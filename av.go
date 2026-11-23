package main

// JT/T 1078 音视频推流：内置 H.264 测试片段（testsrc 352x288 15fps）循环 + G.711A 正弦音

import (
	"bytes"
	_ "embed"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed assets/clip.h264
var clipH264 []byte

func videoClip() []byte { return clipH264 }

type vframe struct {
	data []byte
	key  bool
}

var (
	framesOnce sync.Once
	clipFrames []vframe
)

// frames 把 Annex-B 码流切成访问单元：SPS/PPS/SEI 与随后的 IDR 合成一个 I 帧
func frames() []vframe {
	framesOnce.Do(func() {
		var nalus [][]byte
		b := clipH264
		for {
			i := bytes.Index(b, []byte{0, 0, 1})
			if i < 0 {
				break
			}
			j := bytes.Index(b[i+3:], []byte{0, 0, 1})
			end := len(b)
			if j >= 0 {
				end = i + 3 + j
				if b[end-1] == 0 {
					end--
				}
			}
			nalus = append(nalus, b[i:end])
			b = b[end:]
			if j < 0 {
				break
			}
		}
		var cur []byte
		key := false
		for _, n := range nalus {
			if len(n) < 4 {
				continue
			}
			typ := n[3] & 0x1F                 // n 以 00 00 01 开头
			cur = append(append(cur, 0), n...) // 统一为 4 字节起始码
			switch typ {
			case 5:
				key = true
				fallthrough
			case 1:
				clipFrames = append(clipFrames, vframe{cur, key})
				cur, key = nil, false
			}
		}
	})
	return clipFrames
}

// alaw 16bit PCM -> G.711A
func alaw(s int16) byte {
	mask := byte(0xD5)
	v := int(s)
	if v < 0 {
		mask, v = 0x55, -v-1
	}
	v = min(v, 32635)
	var seg byte
	for x := v >> 8; x > 0 && seg < 7; x >>= 1 {
		seg++
	}
	var out byte
	if seg == 0 {
		out = byte(v>>4) & 0x0F
	} else {
		out = seg<<4 | byte(v>>(seg+3))&0x0F
	}
	return out ^ mask
}

type rtpWriter struct {
	conn    net.Conn
	sim     []byte
	channel int
	seq     uint16
	lastI   time.Time
	lastF   time.Time
	frames  int64
}

// packet 1078 RTP 包：30316364 V/P/X/CC M/PT 序号 SIM 通道 类型/分包 时间戳 [I帧间隔 帧间隔] 长度 数据
func (w *rtpWriter) write(pt byte, dtype int, ts time.Time, data []byte) error {
	const maxBody = 950
	n := (len(data) + maxBody - 1) / maxBody
	for i := 0; i < max(n, 1); i++ {
		part := data[i*maxBody : min((i+1)*maxBody, len(data))]
		sub := 0 // 0 原子 1 第一包 2 最后一包 3 中间包
		if n > 1 {
			sub = map[bool]int{true: 1, false: 3}[i == 0]
			if i == n-1 {
				sub = 2
			}
		}
		m := pt
		if i == n-1 {
			m |= 0x80 // 一帧的最后一包置 M
		}
		h := cat([]byte{0x30, 0x31, 0x63, 0x64, 0x81, m}, u16(int(w.seq)), w.sim, []byte{byte(w.channel), byte(dtype<<4 | sub)},
			u32(int(ts.UnixMilli()>>32)), u32(int(ts.UnixMilli())))
		if dtype <= 2 { // 视频帧带间隔
			h = cat(h, u16(int(ts.Sub(w.lastI).Milliseconds())&0xFFFF), u16(int(ts.Sub(w.lastF).Milliseconds())&0xFFFF))
		}
		w.seq++
		w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := w.conn.Write(cat(h, u16(len(part)), part)); err != nil {
			return err
		}
	}
	return nil
}

type avStream struct {
	Channel int    `json:"channel"`
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Frames  int64  `json:"frames"`
	stop    chan struct{}
	once    sync.Once
	paused  atomic.Bool
	speed   atomic.Int32 // 回放倍数
	audio   atomic.Bool
	video   atomic.Bool
	seek    atomic.Int64 // 回放拖动位置(ms)，0 表示无
}

var dataTypeName = map[int]string{0: "音视频", 1: "视频", 2: "双向对讲", 3: "监听", 4: "中心广播", 5: "透传"}

// startStream playStart 非零为回放（时间戳从录像开始时间起算）
func (t *Terminal) startStream(ip string, tcpPort, udpPort, ch, dataType int, kind string, playStart, playEnd time.Time, speed int) error {
	network, port := "tcp", tcpPort
	if port == 0 {
		network, port = "udp", udpPort
	}
	t.mu.Lock()
	if h := strings.TrimSpace(t.Cfg.AVHost); h != "" {
		ip = h
	}
	t.mu.Unlock()
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	conn, err := net.DialTimeout(network, addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("连接媒体服务器 %s 失败: %v", addr, err)
	}
	t.mu.Lock()
	if old := t.streams[ch]; old != nil {
		old.close()
	}
	s := &avStream{Channel: ch, Kind: kind + " " + dataTypeName[dataType], Target: network + "://" + addr, stop: make(chan struct{})}
	s.speed.Store(int32(max(speed, 1)))
	s.audio.Store(dataType == 0 || dataType == 2 || dataType == 3)
	s.video.Store(dataType <= 1)
	t.streams[ch] = s
	simLen := 6
	if t.Cfg.ver() >= V2019 {
		simLen = 10
	}
	sim := bcd(t.Cfg.Phone, simLen)
	t.mu.Unlock()
	t.logf("通道 %d 开始推流 %s -> %s", ch, s.Kind, s.Target)
	go t.readAVDown(s, conn)
	go t.pump(s, conn, &rtpWriter{conn: conn, sim: sim, channel: ch}, playStart, playEnd)
	return nil
}

// pump 按 15fps 发视频帧，每 40ms 发 320 字节 G.711A；回放时间戳从 playStart 起按倍速推进
func (t *Terminal) pump(s *avStream, conn net.Conn, w *rtpWriter, playStart, playEnd time.Time) {
	defer func() {
		conn.Close()
		t.mu.Lock()
		if t.streams[s.Channel] == s {
			delete(t.streams, s.Channel)
		}
		t.mu.Unlock()
		t.logf("通道 %d 推流结束，共 %d 帧", s.Channel, atomic.LoadInt64(&w.frames))
	}()
	fs := frames()
	if len(fs) == 0 {
		t.errf("内置视频片段无可用帧")
		return
	}
	go func() { // 读走服务器下发数据（对讲音频等），连接断开时结束推流
		buf := make([]byte, 4096)
		for {
			if _, err := conn.Read(buf); err != nil {
				s.close()
				return
			}
		}
	}()
	start := time.Now()
	media := playStart
	if media.IsZero() {
		media = start
	}
	vt := time.NewTicker(time.Second / 15)
	at := time.NewTicker(40 * time.Millisecond)
	defer vt.Stop()
	defer at.Stop()
	fi := 0
	var liveSeq uint64
	liveNeedKey := true
	w.lastI, w.lastF = media, media
	sentVideo := !s.video.Load() // 音视频流以 I 帧开头，首帧视频发出前不发音频
	for {
		select {
		case <-s.stop:
			return
		case <-vt.C:
			if s.paused.Load() || !s.video.Load() {
				continue
			}
			if ms := s.seek.Swap(0); ms > 0 {
				media, fi = time.UnixMilli(ms), 0
			}
			sp := time.Duration(s.speed.Load())
			media = media.Add(time.Second / 15 * sp)
			if !playEnd.IsZero() && media.After(playEnd) {
				return
			}
			var f vframe
			if lf, hasFrame, live := t.liveH264Next(s.Channel, liveSeq, liveNeedKey); live {
				if !hasFrame {
					continue // 实时流活跃但本拍未到新帧（或等待关键帧），跳过本拍避免回退彩条闪烁
				}
				liveSeq = lf.seq
				liveNeedKey = false
				f = vframe{data: lf.data, key: lf.key}
			} else {
				liveSeq = 0
				liveNeedKey = true
				f = fs[fi%len(fs)]
				fi++
			}
			dtype := 1 // P 帧
			if f.key {
				dtype = 0
				w.lastI = media
			}
			if err := w.write(98, dtype, media, f.data); err != nil {
				t.errf("通道 %d 推流中断: %v", s.Channel, err)
				return
			}
			w.lastF = media
			sentVideo = true
			n := atomic.AddInt64(&w.frames, 1)
			t.mu.Lock()
			s.Frames = n
			t.mu.Unlock()
		case <-at.C:
			if s.paused.Load() || !s.audio.Load() || (s.video.Load() && !sentVideo) {
				continue
			}
			if !s.video.Load() {
				media = media.Add(time.Duration(40*max(int(s.speed.Load()), 1)) * time.Millisecond)
			}
			var pcm []byte
			if mic, ok := t.popTalkUp(320); ok {
				pcm = mic
			} else {
				pcm = bytes.Repeat([]byte{0xD5}, 320) // 未开启麦克风时发送 G.711A 静音，避免 440Hz 持续蜂鸣杂音
			}
			if err := w.write(6, 3, media, pcm); err != nil { // PT 6 = G.711A，数据类型 3 = 音频帧
				return
			}
			if !s.video.Load() {
				n := atomic.AddInt64(&w.frames, 1)
				t.mu.Lock()
				s.Frames = n
				t.mu.Unlock()
			}
		}
	}
}

func (s *avStream) close() { s.once.Do(func() { close(s.stop) }) }

func (t *Terminal) stopStream(ch int) bool {
	t.mu.Lock()
	s := t.streams[ch]
	delete(t.streams, ch)
	t.mu.Unlock()
	if s != nil {
		s.close()
	}
	return s != nil
}

func (t *Terminal) stopStreams() {
	t.mu.Lock()
	var chs []int
	for ch := range t.streams {
		chs = append(chs, ch)
	}
	t.mu.Unlock()
	for _, ch := range chs {
		t.stopStream(ch)
	}
}

func (t *Terminal) stream(ch int) *avStream {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.streams[ch]
}
