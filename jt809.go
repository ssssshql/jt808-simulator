package main

// JT/T 809 平台间数据交换协议模拟（下级平台 Client + 上级平台 Server）：
// - 帧格式：0x5B + 头(22B) + 体(可选加密) + CRC16-CCITT(2B) + 0x5D
// - 转义：0x5B->5A01, 0x5A->5A02, 0x5D->5E01, 0x5E->5E02
// - 支持 0x1001/1002 登录、0x1005/1006 心跳、0x1200(1201/1202)、0x1400(1402) 等常用业务报文

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"jt808sim/internal/codec"
)

var jt809MsgNames = map[uint16]string{
	0x1001: "主链路登录请求", 0x1002: "主链路登录应答",
	0x1003: "主链路注销请求", 0x1004: "主链路注销应答",
	0x1005: "主链路连接保持请求", 0x1006: "主链路连接保持应答",
	0x1200: "主链路车辆动态信息交换", 0x1201: "上传车辆注册信息", 0x1202: "实时上传车辆定位信息",
	0x1300: "主链路平台间信息交互", 0x1400: "主链路报警信息交互", 0x1402: "上报报警信息",
	0x1500: "主链路车辆监管", 0x1600: "主链路车辆静态信息",
	0x9001: "从链路连接请求", 0x9002: "从链路连接应答",
}

type jt809Config struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	ListenPort int    `json:"listenPort"`
	CenterID   uint32 `json:"centerId"`
	UserID     uint32 `json:"userId"`
	Password   string `json:"password"`
	DownIP     string `json:"downIp"`
	DownPort   int    `json:"downPort"`
	Encrypt    bool   `json:"encrypt"`
	M1         uint32 `json:"m1"`
	IA1        uint32 `json:"ia1"`
	IC1        uint32 `json:"ic1"`
	Plate      string `json:"plate"`
	PlateColor int    `json:"plateColor"`
}

type jt809Log struct {
	T    string `json:"t"`
	Role string `json:"role"` // client / server
	Dir  string `json:"dir"`  // up / down / sys
	ID   string `json:"id"`
	Name string `json:"name"`
	Len  int    `json:"len"`
	Hex  string `json:"hex"`
}

type jt809Manager struct {
	mu           sync.Mutex
	cfg          jt809Config
	cliStatus    string // offline / connecting / online
	cliConn      net.Conn
	cliStop      chan struct{}
	cliSN        uint32
	cliUp, cliDn int64
	srvRunning   bool
	srvLn        net.Listener
	srvClients   int
	srvRx, srvTx int64
	logs         []jt809Log
}

var jt809 = &jt809Manager{
	cfg: jt809Config{
		Host: "127.0.0.1", Port: 32019, ListenPort: 32020,
		CenterID: 101, UserID: 101, Password: "123456",
		DownIP: "127.0.0.1", DownPort: 32021,
		M1: 1, IA1: 1, IC1: 1, Plate: "粤A18888", PlateColor: 2,
	},
	cliStatus: "offline",
}

func crc16CCITT809(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func crypt809(key, m1, ia1, ic1 uint32, data []byte) []byte {
	out := make([]byte, len(data))
	copy(out, data)
	if m1 == 0 {
		m1 = 1
	}
	if key == 0 {
		key = 1
	}
	for i := range out {
		key = ia1*(key%m1) + ic1
		out[i] ^= byte((key >> 20) & 0xFF)
	}
	return out
}

func escape809(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b) + 8)
	for _, c := range b {
		switch c {
		case 0x5B:
			out.Write([]byte{0x5A, 0x01})
		case 0x5A:
			out.Write([]byte{0x5A, 0x02})
		case 0x5D:
			out.Write([]byte{0x5E, 0x01})
		case 0x5E:
			out.Write([]byte{0x5E, 0x02})
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes()
}

func unescape809(b []byte) ([]byte, bool) {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case 0x5A:
			if i+1 >= len(b) {
				return nil, false
			}
			switch b[i+1] {
			case 0x01:
				out = append(out, 0x5B)
			case 0x02:
				out = append(out, 0x5A)
			default:
				return nil, false
			}
			i++
		case 0x5E:
			if i+1 >= len(b) {
				return nil, false
			}
			switch b[i+1] {
			case 0x01:
				out = append(out, 0x5D)
			case 0x02:
				out = append(out, 0x5E)
			default:
				return nil, false
			}
			i++
		default:
			out = append(out, b[i])
		}
	}
	return out, true
}

func build809Frame(msgID uint16, sn, centerID uint32, encrypt bool, key, m1, ia1, ic1 uint32, body []byte) []byte {
	payload := body
	var encFlag byte
	if encrypt && len(body) > 0 {
		encFlag = 1
		payload = crypt809(key, m1, ia1, ic1, body)
	}
	totalLen := uint32(22 + len(payload) + 2 + 2) // 含首尾 0x5B/0x5D
	raw := make([]byte, 22+len(payload)+2)
	binary.BigEndian.PutUint32(raw[0:4], totalLen)
	binary.BigEndian.PutUint32(raw[4:8], sn)
	binary.BigEndian.PutUint16(raw[8:10], msgID)
	binary.BigEndian.PutUint32(raw[10:14], centerID)
	raw[14], raw[15], raw[16] = 1, 0, 0
	raw[17] = encFlag
	binary.BigEndian.PutUint32(raw[18:22], key)
	copy(raw[22:], payload)
	crc := crc16CCITT809(raw[:22+len(payload)])
	binary.BigEndian.PutUint16(raw[22+len(payload):], crc)
	esc := escape809(raw)
	frame := make([]byte, 0, len(esc)+2)
	frame = append(frame, 0x5B)
	frame = append(frame, esc...)
	frame = append(frame, 0x5D)
	return frame
}

type msg809 struct {
	ID       uint16
	SN       uint32
	CenterID uint32
	Encrypt  bool
	Key      uint32
	Body     []byte
	CRCOK    bool
}

func parse809Frame(inner []byte, m1, ia1, ic1 uint32) *msg809 {
	raw, ok := unescape809(inner)
	if !ok || len(raw) < 24 {
		return nil
	}
	n := len(raw) - 2
	want := binary.BigEndian.Uint16(raw[n:])
	got := crc16CCITT809(raw[:n])
	body := raw[22:n]
	enc := raw[17] != 0
	key := binary.BigEndian.Uint32(raw[18:22])
	if enc && len(body) > 0 {
		body = crypt809(key, m1, ia1, ic1, body)
	}
	return &msg809{
		ID:       binary.BigEndian.Uint16(raw[8:10]),
		SN:       binary.BigEndian.Uint32(raw[4:8]),
		CenterID: binary.BigEndian.Uint32(raw[10:14]),
		Encrypt:  enc,
		Key:      key,
		Body:     body,
		CRCOK:    want == got,
	}
}

func (m *jt809Manager) addLog(role, dir string, id uint16, name string, raw []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name == "" {
		name = jt809MsgNames[id]
		if name == "" {
			name = "未知报文"
		}
	}
	h := ""
	if len(raw) > 0 {
		h = strings.ToUpper(hex.EncodeToString(raw[:min(len(raw), 512)]))
	}
	m.logs = append(m.logs, jt809Log{
		T: time.Now().Format("15:04:05"), Role: role, Dir: dir,
		ID: fmt.Sprintf("%04X", id), Name: name, Len: len(raw), Hex: h,
	})
	if len(m.logs) > 120 {
		m.logs = m.logs[len(m.logs)-120:]
	}
}

func (m *jt809Manager) startClient(cfg jt809Config) error {
	m.stopClient()
	m.mu.Lock()
	m.cfg = cfg
	m.cliStatus = "connecting"
	stop := make(chan struct{})
	m.cliStop = stop
	m.mu.Unlock()
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, 8*time.Second)
	if err != nil {
		m.mu.Lock()
		m.cliStatus = "offline"
		m.mu.Unlock()
		m.addLog("client", "sys", 0, "连接失败: "+err.Error(), nil)
		return err
	}
	m.mu.Lock()
	m.cliConn = conn
	m.mu.Unlock()
	// 发送 0x1001 主链路登录请求（UserID 4B + Password 8B + DownIP 32B + DownPort 2B）
	body := make([]byte, 46)
	binary.BigEndian.PutUint32(body[0:4], cfg.UserID)
	copy(body[4:12], codec.Fixed(cfg.Password, 8))
	copy(body[12:44], codec.Fixed(cfg.DownIP, 32))
	binary.BigEndian.PutUint16(body[44:46], uint16(cfg.DownPort))
	if err := m.sendClient(0x1001, body); err != nil {
		m.stopClient()
		return err
	}
	go m.clientLoop(conn, stop)
	return nil
}

func (m *jt809Manager) stopClient() {
	m.mu.Lock()
	stop, conn := m.cliStop, m.cliConn
	m.cliStop, m.cliConn = nil, nil
	m.cliStatus = "offline"
	m.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if conn != nil {
		conn.Close()
	}
}

func (m *jt809Manager) sendClient(id uint16, body []byte) error {
	m.mu.Lock()
	conn, cfg := m.cliConn, m.cfg
	if conn == nil {
		m.mu.Unlock()
		return fmt.Errorf("下级平台未连接")
	}
	m.cliSN++
	sn := m.cliSN
	frame := build809Frame(id, sn, cfg.CenterID, cfg.Encrypt, sn, cfg.M1, cfg.IA1, cfg.IC1, body)
	m.cliUp++
	m.mu.Unlock()
	if _, err := conn.Write(frame); err != nil {
		return err
	}
	m.addLog("client", "up", id, "", frame)
	return nil
}

func (m *jt809Manager) clientLoop(conn net.Conn, stop chan struct{}) {
	defer func() {
		conn.Close()
		m.mu.Lock()
		if m.cliConn == conn {
			m.cliConn = nil
			m.cliStatus = "offline"
		}
		m.mu.Unlock()
	}()
	go func() {
		tk := time.NewTicker(30 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				m.mu.Lock()
				online := m.cliStatus == "online"
				m.mu.Unlock()
				if online {
					_ = m.sendClient(0x1005, nil)
				}
			}
		}
	}()
	read809Stream(conn, func(inner []byte, raw []byte) {
		m.mu.Lock()
		cfg := m.cfg
		m.cliDn++
		m.mu.Unlock()
		msg := parse809Frame(inner, cfg.M1, cfg.IA1, cfg.IC1)
		if msg == nil {
			return
		}
		m.addLog("client", "down", msg.ID, "", raw)
		if msg.ID == 0x1002 && len(msg.Body) >= 1 && msg.Body[0] == 0 {
			m.mu.Lock()
			m.cliStatus = "online"
			m.mu.Unlock()
			m.addLog("client", "sys", 0x1002, "主链路登录成功", nil)
		}
	})
}

func (m *jt809Manager) startServer(port int) error {
	m.stopServer()
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg.ListenPort = port
	m.srvLn = ln
	m.srvRunning = true
	m.mu.Unlock()
	m.addLog("server", "sys", 0, fmt.Sprintf("上级平台开始监听 :%d", port), nil)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			m.mu.Lock()
			m.srvClients++
			m.mu.Unlock()
			go m.serve809Conn(c)
		}
	}()
	return nil
}

func (m *jt809Manager) stopServer() {
	m.mu.Lock()
	ln := m.srvLn
	m.srvLn = nil
	m.srvRunning = false
	m.srvClients = 0
	m.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

func (m *jt809Manager) serve809Conn(c net.Conn) {
	defer func() {
		c.Close()
		m.mu.Lock()
		if m.srvClients > 0 {
			m.srvClients--
		}
		m.mu.Unlock()
	}()
	read809Stream(c, func(inner, raw []byte) {
		m.mu.Lock()
		cfg := m.cfg
		m.srvRx++
		m.mu.Unlock()
		msg := parse809Frame(inner, cfg.M1, cfg.IA1, cfg.IC1)
		if msg == nil {
			return
		}
		m.addLog("server", "down", msg.ID, "", raw)
		var respID uint16
		var respBody []byte
		switch msg.ID {
		case 0x1001:
			respID = 0x1002
			respBody = []byte{0, 0, 0, 0, 1} // result=0 成功 + verifyCode=1
		case 0x1003:
			respID = 0x1004
		case 0x1005:
			respID = 0x1006
		}
		if respID != 0 {
			frame := build809Frame(respID, msg.SN, msg.CenterID, false, 0, cfg.M1, cfg.IA1, cfg.IC1, respBody)
			if _, err := c.Write(frame); err == nil {
				m.mu.Lock()
				m.srvTx++
				m.mu.Unlock()
				m.addLog("server", "up", respID, "", frame)
			}
		}
	})
}

func read809Stream(c net.Conn, onFrame func(inner, raw []byte)) {
	tmp := make([]byte, 4096)
	var buf []byte
	for {
		n, err := c.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				s := bytes.IndexByte(buf, 0x5B)
				if s < 0 {
					buf = buf[:0]
					break
				}
				if s > 0 {
					buf = buf[s:]
				}
				e := bytes.IndexByte(buf[1:], 0x5D)
				if e < 0 {
					break
				}
				end := 1 + e
				onFrame(buf[1:end], buf[:end+1])
				buf = buf[end+1:]
			}
			if len(buf) > 65536 {
				buf = buf[:0]
			}
		}
		if err != nil {
			return
		}
	}
}

// build809BizBody 构造常用 809 车辆动态/报警交换子业务报文体（0x1202 / 0x1201 / 0x1402）
func build809BizBody(subID uint16, plate string, color int) []byte {
	var data []byte
	switch subID {
	case 0x1202: // 实时定位：加密(1) + 日月年(4) + 时分秒(3) + 经度(4) + 纬度(4) + 速度(2) + 行驶速度(2) + 里程(4) + 方向(2) + 海拔(2) + 状态(4) + 报警(4) = 36B
		now := time.Now()
		data = make([]byte, 36)
		data[1], data[2] = byte(now.Day()), byte(now.Month())
		binary.BigEndian.PutUint16(data[3:5], uint16(now.Year()))
		data[5], data[6], data[7] = byte(now.Hour()), byte(now.Minute()), byte(now.Second())
		binary.BigEndian.PutUint32(data[8:12], 113324500)
		binary.BigEndian.PutUint32(data[12:16], 23106500)
		binary.BigEndian.PutUint16(data[16:18], 60)
		binary.BigEndian.PutUint16(data[18:20], 60)
		binary.BigEndian.PutUint32(data[20:24], 1234)
		binary.BigEndian.PutUint16(data[24:26], 90)
		binary.BigEndian.PutUint16(data[26:28], 20)
		binary.BigEndian.PutUint32(data[28:32], 3) // ACC+定位
	case 0x1402: // 上报报警信息：信息来源(1) + 报警类型(2) + 报警时间(8) + 信息ID(4) + 信息长度(4) = 19B
		data = make([]byte, 19)
		data[0] = 1
		binary.BigEndian.PutUint16(data[1:3], 0x0001) // 超速报警
		binary.BigEndian.PutUint64(data[3:11], uint64(time.Now().Unix()))
		binary.BigEndian.PutUint32(data[11:15], 1)
	default: // 0x1201 车辆注册：平台ID(11) + 厂商(11) + 型号(8) + 终端ID(7) + SIM(12)
		data = make([]byte, 49)
		copy(data[0:11], "34000010001")
		copy(data[11:22], "AUTOT")
		copy(data[22:30], "JT808SIM")
		copy(data[30:37], "0000001")
		copy(data[37:49], "013800000001")
	}
	hdr := make([]byte, 28) // 车牌(21) + 颜色(1) + 子业务(2) + 后续长度(4)
	copy(hdr[0:21], codec.Fixed(plate, 21))
	hdr[21] = byte(color)
	binary.BigEndian.PutUint16(hdr[22:24], subID)
	binary.BigEndian.PutUint32(hdr[24:28], uint32(len(data)))
	return append(hdr, data...)
}

func registerJT809API(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jt809", func(w http.ResponseWriter, r *http.Request) {
		jt809.mu.Lock()
		res := map[string]any{
			"cfg":        jt809.cfg,
			"cliStatus":  jt809.cliStatus,
			"cliUp":      jt809.cliUp,
			"cliDn":      jt809.cliDn,
			"srvRunning": jt809.srvRunning,
			"srvClients": jt809.srvClients,
			"srvRx":      jt809.srvRx,
			"srvTx":      jt809.srvTx,
			"logs":       append([]jt809Log(nil), jt809.logs...),
		}
		jt809.mu.Unlock()
		writeJSON(w, 200, res)
	})
	mux.HandleFunc("POST /api/jt809", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "JSON 无效"})
			return
		}
		op := pstr(p, "op", "")
		var err error
		switch op {
		case "startClient":
			jt809.mu.Lock()
			cfg := jt809.cfg
			jt809.mu.Unlock()
			cfg.Host = pstr(p, "host", cfg.Host)
			cfg.Port = pint(p, "port", cfg.Port)
			cfg.CenterID = uint32(pint(p, "centerId", int(cfg.CenterID)))
			cfg.UserID = uint32(pint(p, "userId", int(cfg.UserID)))
			cfg.Password = pstr(p, "password", cfg.Password)
			cfg.Plate = pstr(p, "plate", cfg.Plate)
			cfg.PlateColor = pint(p, "plateColor", cfg.PlateColor)
			cfg.Encrypt = pint(p, "encrypt", 0) == 1
			cfg.M1 = uint32(max(1, pint(p, "m1", int(cfg.M1))))
			cfg.IA1 = uint32(max(1, pint(p, "ia1", int(cfg.IA1))))
			cfg.IC1 = uint32(max(1, pint(p, "ic1", int(cfg.IC1))))
			err = jt809.startClient(cfg)
		case "stopClient":
			jt809.stopClient()
		case "startServer":
			err = jt809.startServer(pint(p, "listenPort", jt809.cfg.ListenPort))
		case "stopServer":
			jt809.stopServer()
		case "send":
			sub := strings.ToUpper(strings.TrimPrefix(pstr(p, "subId", "1202"), "0X"))
			jt809.mu.Lock()
			plate, color := jt809.cfg.Plate, jt809.cfg.PlateColor
			jt809.mu.Unlock()
			switch sub {
			case "1005":
				err = jt809.sendClient(0x1005, nil)
			case "1201":
				err = jt809.sendClient(0x1200, build809BizBody(0x1201, plate, color))
			case "1202":
				err = jt809.sendClient(0x1200, build809BizBody(0x1202, plate, color))
			case "1402":
				err = jt809.sendClient(0x1400, build809BizBody(0x1402, plate, color))
			default:
				var raw []byte
				if h := cleanHexInput(pstr(p, "bodyHex", "")); h != "" {
					raw, err = hex.DecodeString(h)
				}
				if err == nil {
					var id64 uint64
					if id64, err = parseHexU16(pstr(p, "msgId", "1200")); err == nil {
						err = jt809.sendClient(uint16(id64), raw)
					}
				}
			}
		case "clearLog":
			jt809.mu.Lock()
			jt809.logs = nil
			jt809.mu.Unlock()
		default:
			err = fmt.Errorf("未知操作 %s", op)
		}
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
}

func parseHexU16(s string) (uint64, error) {
	s = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "0X")
	var v uint64
	_, err := fmt.Sscanf(s, "%X", &v)
	return v, err
}
