package main

// 压力测试：批量轻量虚拟终端（注册 / 鉴权 / 心跳 / 位置汇报），统计在线数、收发速率、应答、超时、重连与应答延迟。
// 虚拟终端不使用完整的 Terminal 模型：每个只有一个连接、一个读协程和很小的待应答表，2000 个约几十 MB 内存。

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	stressMaxCount = 2000
	stressMaxRamp  = 500
)

type StressConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Transport  string `json:"transport"` // tcp / udp
	Version    string `json:"version"`   // 2013 / 2019
	Count      int    `json:"count"`
	PhoneStart string `json:"phoneStart"`
	Interval   int    `json:"interval"`   // 位置汇报间隔（秒）
	Heartbeat  int    `json:"heartbeat"`  // 心跳间隔（秒）
	Ramp       int    `json:"ramp"`       // 每秒启动的终端数
	AckTimeout int    `json:"ackTimeout"` // 应答超时（秒）
	Duration   int    `json:"duration"`   // 运行时长（秒，0 = 直到手动停止）
}

func (c *StressConfig) normalize() error {
	c.Host = strings.TrimSpace(c.Host)
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	if c.Transport == "" {
		c.Transport = "tcp"
	}
	if c.Version == "" {
		c.Version = "2019"
	}
	def := func(p *int, v int) {
		if *p == 0 {
			*p = v
		}
	}
	def(&c.Count, 100)
	def(&c.Interval, 10)
	def(&c.Heartbeat, 30)
	def(&c.Ramp, 50)
	def(&c.AckTimeout, 10)
	if c.PhoneStart == "" {
		c.PhoneStart = "19900000001"
	}
	switch {
	case c.Host == "":
		return fmt.Errorf("请填写目标服务器地址")
	case c.Port <= 0 || c.Port > 65535:
		return fmt.Errorf("端口无效")
	case c.Transport != "tcp" && c.Transport != "udp":
		return fmt.Errorf("传输方式只能是 tcp/udp")
	case c.Version != "2013" && c.Version != "2019":
		return fmt.Errorf("协议版本只能是 2013/2019")
	case c.Count < 1 || c.Count > stressMaxCount:
		return fmt.Errorf("终端数量 1~%d", stressMaxCount)
	case c.Ramp < 1 || c.Ramp > stressMaxRamp:
		return fmt.Errorf("启动速率 1~%d 个/秒", stressMaxRamp)
	case c.Interval < 1 || c.Interval > 3600:
		return fmt.Errorf("汇报间隔 1~3600 秒")
	case c.Heartbeat < 5 || c.Heartbeat > 3600:
		return fmt.Errorf("心跳间隔 5~3600 秒")
	case c.AckTimeout < 1 || c.AckTimeout > 60:
		return fmt.Errorf("应答超时 1~60 秒")
	case c.Duration < 0 || c.Duration > 7*86400:
		return fmt.Errorf("运行时长 0~604800 秒")
	case strings.Trim(c.PhoneStart, "0123456789") != "":
		return fmt.Errorf("起始手机号只能是数字")
	}
	maxLen := 20
	if c.Version == "2013" {
		maxLen = 12
	}
	if len(c.PhoneStart) > maxLen {
		return fmt.Errorf("起始手机号最多 %d 位", maxLen)
	}
	if last := stressPhone(c.PhoneStart, c.Count-1); len(last) > len(c.PhoneStart) {
		return fmt.Errorf("手机号范围溢出：%s 起 %d 个超出 %d 位", c.PhoneStart, c.Count, len(c.PhoneStart))
	}
	return nil
}

// stressPhone 起始手机号 + i（支持 20 位超 uint64 号码，保持位数与前导零）
func stressPhone(start string, i int) string {
	var n big.Int
	if _, ok := n.SetString(start, 10); !ok {
		return start
	}
	n.Add(&n, big.NewInt(int64(i)))
	s := n.String()
	if len(s) < len(start) {
		s = strings.Repeat("0", len(start)-len(s)) + s
	}
	return s
}

// 应答延迟直方图分桶上限（毫秒）
var latBuckets = []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, math.Inf(1)}

type stressSample struct {
	T         int64   `json:"t"`
	Sent      float64 `json:"sent"` // 条/秒
	Recv      float64 `json:"recv"`
	Online    int64   `json:"online"`
	Connected int64   `json:"connected"`
	LatP95    float64 `json:"latP95"`
}

type Stress struct {
	mu      sync.Mutex
	cfg     StressConfig
	running bool
	stop    chan struct{}
	wg      sync.WaitGroup
	started time.Time
	ended   time.Time
	lastErr string
	series  []stressSample
	cur     stressSample
	reason  string // 停止原因

	// 报告用：全程采样（超过 3600 点时隔点抽稀，stride 秒一个点）与峰值/均值
	all                []stressSample
	stride, tick       int
	peakOn, peakConn   int64
	peakSent, peakRecv float64
	onSum, onN         int64
	lastReport         *StressReport
	resMu              sync.Mutex
	res                []float32 // 应答延迟蓄水池抽样（毫秒），报告计算精确分位数
	resN               int64

	spawned, connected, online                     atomic.Int64
	sent, recv, acks, nacks, timeouts, reconnects  atomic.Int64
	regOK, regFail, authFail, connFail, sendErrors atomic.Int64
	latSum, latN, latMax                           atomic.Int64 // 微秒
	hist                                           [14]atomic.Int64
	winHist                                        [14]atomic.Int64 // 当前 1 秒窗口
}

var stress = &Stress{}

func (s *Stress) Start(c StressConfig) error {
	if err := c.normalize(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("压力测试正在运行")
	}
	s.mu.Unlock()
	s.wg.Wait() // 释放锁后再等待上一轮的虚拟终端全部退出，避免与 setErr 死锁
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("压力测试正在运行")
	}
	s.cfg, s.running, s.stop, s.started, s.ended, s.lastErr = c, true, make(chan struct{}), time.Now(), time.Time{}, ""
	s.series, s.cur, s.reason = nil, stressSample{}, ""
	s.all, s.stride, s.tick, s.peakOn, s.peakConn, s.peakSent, s.peakRecv, s.onSum, s.onN = nil, 1, 0, 0, 0, 0, 0, 0, 0
	for _, a := range []*atomic.Int64{&s.spawned, &s.connected, &s.online, &s.sent, &s.recv, &s.acks, &s.nacks, &s.timeouts, &s.reconnects,
		&s.regOK, &s.regFail, &s.authFail, &s.connFail, &s.sendErrors, &s.latSum, &s.latN, &s.latMax} {
		a.Store(0)
	}
	for i := range s.hist {
		s.hist[i].Store(0)
		s.winHist[i].Store(0)
	}
	s.resMu.Lock()
	s.res, s.resN = nil, 0
	s.resMu.Unlock()
	stop := s.stop
	s.wg.Add(1)
	go s.spawnLoop(c, stop)
	go s.sampleLoop(stop)
	hub.add(LogEntry{Dir: "sys", Summary: fmt.Sprintf("压力测试开始：%d 个终端 → %s:%d/%s，版本 %s，汇报 %ds，启动 %d 个/秒",
		c.Count, c.Host, c.Port, strings.ToUpper(c.Transport), c.Version, c.Interval, c.Ramp)})
	return nil
}

func (s *Stress) Stop(reason string) {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.ended = time.Now()
	s.reason = reason
	close(s.stop)
	s.mu.Unlock()
	if r, err := s.saveReport(); err != nil {
		errf("保存压测报告失败: %v", err)
	} else {
		hub.add(LogEntry{Dir: "sys", Summary: "压测报告已生成：" + r.ID})
	}
	hub.add(LogEntry{Dir: "sys", Summary: fmt.Sprintf("压力测试停止（%s）：发送 %d 条，接收 %d 条，应答 %d，超时 %d，重连 %d",
		reason, s.sent.Load(), s.recv.Load(), s.acks.Load(), s.timeouts.Load(), s.reconnects.Load())})
}

func (s *Stress) setErr(e string) {
	s.mu.Lock()
	s.lastErr = e
	s.mu.Unlock()
}

func (s *Stress) spawnLoop(c StressConfig, stop chan struct{}) {
	defer s.wg.Done()
	gap := time.Second / time.Duration(c.Ramp)
	for i := 0; i < c.Count; i++ {
		select {
		case <-stop:
			return
		default:
		}
		s.wg.Add(1)
		s.spawned.Add(1)
		go s.runVT(c, i, stop)
		time.Sleep(gap)
	}
	if c.Duration > 0 {
		select {
		case <-stop:
		case <-time.After(time.Duration(c.Duration)*time.Second - time.Since(s.started)):
			s.Stop("达到运行时长")
		}
	}
}

func (s *Stress) sampleLoop(stop chan struct{}) {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	lastSent, lastRecv, last := int64(0), int64(0), time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-tk.C:
			dt := now.Sub(last).Seconds()
			last = now
			sent, recv := s.sent.Load(), s.recv.Load()
			var win [14]int64
			for i := range s.winHist {
				win[i] = s.winHist[i].Swap(0)
			}
			smp := stressSample{T: now.UnixMilli(), Sent: math.Round(float64(sent-lastSent)/dt*10) / 10, Recv: math.Round(float64(recv-lastRecv)/dt*10) / 10,
				Online: s.online.Load(), Connected: s.connected.Load(), LatP95: histQuantile(win[:], 0.95)}
			lastSent, lastRecv = sent, recv
			s.mu.Lock()
			s.cur = smp
			s.series = append(s.series, smp)
			if len(s.series) > 120 {
				s.series = s.series[len(s.series)-120:]
			}
			s.record(smp)
			s.mu.Unlock()
		}
	}
}

func histQuantile(h []int64, q float64) float64 {
	var total int64
	for _, n := range h {
		total += n
	}
	if total == 0 {
		return 0
	}
	need := int64(math.Ceil(float64(total) * q))
	var acc int64
	for i, n := range h {
		acc += n
		if acc >= need {
			if math.IsInf(latBuckets[i], 1) {
				return latBuckets[i-1]
			}
			return latBuckets[i]
		}
	}
	return latBuckets[len(latBuckets)-2]
}

func (s *Stress) observe(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000
	for us := d.Microseconds(); ; {
		cur := s.latMax.Load()
		if us <= cur || s.latMax.CompareAndSwap(cur, us) {
			break
		}
	}
	s.latSum.Add(d.Microseconds())
	s.latN.Add(1)
	s.sampleLat(float32(ms))
	for i, b := range latBuckets {
		if ms <= b {
			s.hist[i].Add(1)
			s.winHist[i].Add(1)
			return
		}
	}
}

// View 页面显示的统计
func (s *Stress) View() map[string]any {
	s.mu.Lock()
	cfg, running, started, ended, lastErr, cur := s.cfg, s.running, s.started, s.ended, s.lastErr, s.cur
	series := append([]stressSample(nil), s.series...)
	s.mu.Unlock()
	var hist [14]int64
	for i := range s.hist {
		hist[i] = s.hist[i].Load()
	}
	avg := 0.0
	if n := s.latN.Load(); n > 0 {
		avg = math.Round(float64(s.latSum.Load())/float64(n)/10) / 100
	}
	elapsed := 0
	if !started.IsZero() {
		end := time.Now()
		if !running && !ended.IsZero() {
			end = ended
		}
		elapsed = int(end.Sub(started).Seconds())
	}
	return map[string]any{
		"running": running, "config": cfg, "elapsed": elapsed, "lastError": lastErr,
		"limits": map[string]int{"maxCount": stressMaxCount, "maxRamp": stressMaxRamp},
		"stats": map[string]any{
			"spawned": s.spawned.Load(), "connected": s.connected.Load(), "online": s.online.Load(),
			"sent": s.sent.Load(), "recv": s.recv.Load(), "acks": s.acks.Load(), "nacks": s.nacks.Load(), "timeouts": s.timeouts.Load(),
			"reconnects": s.reconnects.Load(), "regOK": s.regOK.Load(), "regFail": s.regFail.Load(), "authFail": s.authFail.Load(),
			"connFail": s.connFail.Load(), "sendErrors": s.sendErrors.Load(),
			"sentRate": cur.Sent, "recvRate": cur.Recv,
			"latAvg": avg, "latP50": histQuantile(hist[:], 0.5), "latP95": histQuantile(hist[:], 0.95), "latP99": histQuantile(hist[:], 0.99),
		},
		"series": series,
	}
}

// ---------------- 虚拟终端 ----------------

type vtPend struct {
	id int
	at time.Time
}

type vterm struct {
	s       *Stress
	cfg     *StressConfig
	phone   string
	ver     int
	conn    net.Conn
	wmu     sync.Mutex
	mu      sync.Mutex
	serial  int
	pending map[int]vtPend
	regCh   chan []byte // 8100 消息体
	authCh  chan int    // 0102 的 8001 结果
	token   string
	lat     float64
	lng     float64
	dir     float64
	lastUp  time.Time
	misses  int // 连续超时次数
}

func (s *Stress) runVT(c StressConfig, idx int, stop chan struct{}) {
	defer s.wg.Done()
	v := &vterm{s: s, cfg: &c, phone: stressPhone(c.PhoneStart, idx), ver: V2019, regCh: make(chan []byte, 1), authCh: make(chan int, 1),
		lat: 23.10 + float64(idx%50)*0.002, lng: 113.30 + float64(idx/50)*0.002, dir: rand.Float64() * 360}
	if c.Version == "2013" {
		v.ver = V2013
	}
	backoff := time.Second
	first := true
	for {
		select {
		case <-stop:
			return
		default:
		}
		if !first {
			s.reconnects.Add(1)
			wait := backoff + time.Duration(rand.Int63n(int64(backoff)))
			select {
			case <-stop:
				return
			case <-time.After(wait):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
		first = false
		if v.session(stop) {
			backoff = time.Second // 曾经上线过：重连退避从头开始
		}
	}
}

// session 一次连接：注册（无鉴权码时）→ 鉴权 → 心跳 / 位置汇报，直到断开。返回是否上线过
func (v *vterm) session(stop chan struct{}) bool {
	s, c := v.s, v.cfg
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	conn, err := net.DialTimeout(c.Transport, addr, 10*time.Second)
	if err != nil {
		s.connFail.Add(1)
		s.setErr(err.Error())
		return false
	}
	s.connected.Add(1)
	defer s.connected.Add(-1)
	v.mu.Lock()
	v.conn, v.pending, v.misses = conn, map[int]vtPend{}, 0
	v.mu.Unlock()
	for len(v.regCh) > 0 || len(v.authCh) > 0 { // 丢弃上一次连接遗留的应答
		select {
		case <-v.regCh:
		case <-v.authCh:
		default:
		}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-stop:
		case <-done:
		}
		conn.Close()
	}()
	defer close(done)
	readErr := make(chan error, 1)
	go func() { readErr <- v.readLoop(conn) }()

	timeout := time.Duration(c.AckTimeout) * time.Second
	if v.token == "" {
		if err := v.send(0x0100, v.regBody(), false); err != nil {
			return false
		}
		select {
		case b := <-v.regCh:
			if len(b) < 3 || b[2] != 0 {
				s.regFail.Add(1)
				if len(b) >= 3 {
					s.setErr(fmt.Sprintf("%s 注册失败: %d %s", v.phone, b[2], regResult[b[2]]))
				}
				return false
			}
			s.regOK.Add(1)
			v.token = ungbk(b[3:])
		case <-time.After(timeout):
			s.timeouts.Add(1)
			s.setErr(v.phone + " 注册无应答")
			return false
		case <-readErr:
			return false
		}
	}
	auth := gbk(v.token)
	if v.ver >= V2019 {
		auth = cat(lstr(v.token), fixed(fmt.Sprintf("86%013d", phoneTail(v.phone, 13)), 15), fixed("SIM-STRESS", 20))
	}
	if err := v.send(0x0102, auth, true); err != nil {
		return false
	}
	select {
	case r := <-v.authCh:
		if r != 0 {
			s.authFail.Add(1)
			s.setErr(fmt.Sprintf("%s 鉴权失败 result=%d", v.phone, r))
			v.token = ""
			return false
		}
	case <-time.After(timeout):
		s.timeouts.Add(1)
		s.setErr(v.phone + " 鉴权无应答")
		return false
	case <-readErr:
		return false
	}
	s.online.Add(1)
	defer s.online.Add(-1)

	interval := time.Duration(c.Interval) * time.Second
	hb := time.Duration(c.Heartbeat) * time.Second
	nextRpt := time.Now().Add(time.Duration(rand.Int63n(int64(interval)))) // 错开汇报时刻
	tk := time.NewTicker(250 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return true
		case <-readErr:
			return true
		case now := <-tk.C:
			if !now.Before(nextRpt) {
				nextRpt = nextRpt.Add(interval)
				if nextRpt.Before(now) {
					nextRpt = now.Add(interval)
				}
				if v.send(0x0200, v.locBody(float64(c.Interval)), true) != nil {
					return true
				}
			} else if now.Sub(v.lastSend()) >= hb {
				if v.send(0x0002, nil, true) != nil {
					return true
				}
			}
			if v.expire(now, timeout) {
				s.setErr(v.phone + " 连续 3 次应答超时，重连")
				return true
			}
		}
	}
}

// expire 清理超时的待应答；连续 3 次超时返回 true（判定链路断开）
func (v *vterm) expire(now time.Time, timeout time.Duration) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, p := range v.pending {
		if now.Sub(p.at) > timeout {
			delete(v.pending, k)
			v.s.timeouts.Add(1)
			v.misses++
		}
	}
	return v.misses >= 3
}

func (v *vterm) lastSend() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lastUp
}

func (v *vterm) send(id int, body []byte, track bool) error {
	v.mu.Lock()
	v.serial = (v.serial + 1) & 0xFFFF
	sn, conn := v.serial, v.conn
	if track {
		v.pending[sn] = vtPend{id: id, at: time.Now()}
	}
	v.lastUp = time.Now()
	v.mu.Unlock()
	f := Frame(id, v.phone, sn, body, v.ver, 0, 0)
	v.wmu.Lock()
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := conn.Write(f)
	v.wmu.Unlock()
	if err != nil {
		v.s.sendErrors.Add(1)
		v.s.setErr(err.Error())
		return err
	}
	v.s.sent.Add(1)
	return nil
}

func (v *vterm) readLoop(c net.Conn) error {
	var r FrameReader
	_, udp := c.(*net.UDPConn)
	buf := make([]byte, 2048)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return err
		}
		if udp {
			r = FrameReader{}
		}
		for _, m := range r.Feed(buf[:n]) {
			v.s.recv.Add(1)
			if m.OK {
				v.onDown(m)
			}
		}
	}
}

func (v *vterm) onDown(m *Msg) {
	switch m.ID {
	case 0x8100:
		select {
		case v.regCh <- m.Body:
		default:
		}
	case 0x8001:
		if len(m.Body) < 5 {
			return
		}
		sn, id, res := int(binary.BigEndian.Uint16(m.Body)), int(binary.BigEndian.Uint16(m.Body[2:])), int(m.Body[4])
		v.mu.Lock()
		p, ok := v.pending[sn]
		if ok && p.id == id {
			delete(v.pending, sn)
			v.misses = 0
		}
		v.mu.Unlock()
		if !ok || p.id != id {
			return
		}
		v.s.observe(time.Since(p.at))
		if res == 0 || res == 4 {
			v.s.acks.Add(1)
		} else {
			v.s.nacks.Add(1)
		}
		if id == 0x0102 {
			select {
			case v.authCh <- res:
			default:
			}
		}
	default:
		if m.ID >= 0x8000 { // 其它平台指令一律通用应答成功，避免平台侧堆积重传
			v.send(0x0001, cat(u16(m.Serial), u16(m.ID), []byte{0}), false)
		}
	}
}

func (v *vterm) regBody() []byte {
	plate := fmt.Sprintf("测Y%05d", phoneTail(v.phone, 5))
	devID := v.phone
	if len(devID) > 7 {
		devID = devID[len(devID)-7:]
	}
	head := cat(u16(44), u16(100))
	if v.ver >= V2019 {
		return cat(head, fixed("STRESS", 11), fixed("SIM-STRESS", 30), fixed(devID, 30), []byte{2}, gbk(plate))
	}
	return cat(head, fixed("STRES", 5), fixed("SIM-STRESS", 20), fixed(devID, 7), []byte{2}, gbk(plate))
}

// phoneTail 手机号末 n 位数字
func phoneTail(phone string, n int) uint64 {
	if len(phone) > n {
		phone = phone[len(phone)-n:]
	}
	v, _ := strconv.ParseUint(phone, 10, 64)
	return v
}

// locBody 最小 0200：报警 状态 纬度 经度 高程 速度 方向 时间 + 里程。sec 为距上次汇报秒数，用于推进位置
func (v *vterm) locBody(sec float64) []byte {
	speed := 30 + rand.Float64()*50
	v.dir = math.Mod(v.dir+rand.Float64()*20-10+360, 360)
	v.lat, v.lng = moveBy(v.lat, v.lng, v.dir, speed/3.6*sec)
	return cat(u32(0), u32(1|1<<1|1<<18|1<<19), u32(int(v.lat*1e6)), u32(int(v.lng*1e6)), u16(20), u16(int(speed*10)), u16(int(v.dir)),
		bcdNow(), []byte{0x01, 4}, u32(rand.Intn(100000)))
}
