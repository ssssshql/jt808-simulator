package main

// JT/T 808 帧编解码 + 常用消息体构造（2013 / 2019 双版本）

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	V2013 = 0 // 6 字节 BCD 手机号
	V2019 = 1 // 10 字节 BCD 手机号 + 协议版本号
)

func gbk(s string) []byte {
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return []byte(s)
	}
	return b
}

func ungbk(b []byte) string {
	s, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(s)
}

// bcd 数字串 -> 定长 BCD（左补 0）
func bcd(s string, n int) []byte {
	if len(s) < n*2 {
		s = strings.Repeat("0", n*2-len(s)) + s
	}
	b, _ := hex.DecodeString(s[len(s)-n*2:])
	return b
}

func bcdNow() []byte { return bcd(time.Now().Format("060102150405"), 6) }

// fixed 定长字符串，右补 0x00
func fixed(s string, n int) []byte { return fixedPad(s, n, 0) }

func fixedPad(s string, n int, pad byte) []byte {
	b := gbk(s)
	if len(b) > n {
		b = b[:n]
	}
	return append(b, bytes.Repeat([]byte{pad}, n-len(b))...)
}

// lstr 1 字节长度前缀字符串
func lstr(s string) []byte {
	b := gbk(s)
	return append([]byte{byte(len(b))}, b...)
}

func bcc(b []byte) byte {
	var c byte
	for _, x := range b {
		c ^= x
	}
	return c
}

func escape(b []byte) []byte {
	var out bytes.Buffer
	for _, x := range b {
		switch x {
		case 0x7e:
			out.Write([]byte{0x7d, 0x02})
		case 0x7d:
			out.Write([]byte{0x7d, 0x01})
		default:
			out.WriteByte(x)
		}
	}
	return out.Bytes()
}

func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == 0x7d && i+1 < len(b) {
			out = append(out, b[i+1]+0x7c) // 01->7d, 02->7e
			i++
		} else {
			out = append(out, b[i])
		}
	}
	return out
}

func u16(v int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(v)) }
func u32(v int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }

func cat(parts ...[]byte) []byte {
	var b bytes.Buffer
	for _, p := range parts {
		b.Write(p)
	}
	return b.Bytes()
}

// Frame 组完整帧。total>0 时为分包（total 总包数，index 包序号，从 1 开始）
func Frame(msgID int, phone string, serial int, body []byte, version int, total, index int) []byte {
	return FrameEnc(msgID, phone, serial, body, version, total, index, false)
}

// FrameEnc 同 Frame；rsa=true 时置消息体属性 bit10（消息体已经过 RSA 加密）
func FrameEnc(msgID int, phone string, serial int, body []byte, version int, total, index int, rsa bool) []byte {
	props := len(body) & 0x3FF
	if total > 0 {
		props |= 1 << 13
	}
	if rsa {
		props |= 1 << 10
	}
	var head []byte
	if version >= V2019 {
		props |= 1 << 14
		head = cat(u16(msgID), u16(props), []byte{1}, bcd(phone, 10))
	} else {
		head = cat(u16(msgID), u16(props), bcd(phone, 6))
	}
	head = append(head, u16(serial)...)
	if total > 0 {
		head = cat(head, u16(total), u16(index))
	}
	raw := cat(head, body)
	raw = append(raw, bcc(raw))
	return cat([]byte{0x7e}, escape(raw), []byte{0x7e})
}

type Msg struct {
	ID      int
	Version int
	Phone   string
	Serial  int
	Body    []byte
	OK      bool
	Total   int  // 分包总数（0 = 不分包）
	Index   int  // 包序号，从 1 开始
	RSA     bool // 消息体属性 bit10：消息体经 RSA 加密
	Raw     []byte
}

// Parse raw 为去掉首尾 0x7e 的转义数据
func Parse(raw []byte) *Msg {
	d := unescape(raw)
	if len(d) < 13 {
		return nil
	}
	m := &Msg{OK: bcc(d[:len(d)-1]) == d[len(d)-1]}
	d = d[:len(d)-1]
	m.ID = int(binary.BigEndian.Uint16(d))
	props := int(binary.BigEndian.Uint16(d[2:]))
	m.RSA = props&(1<<10) != 0
	pos := 10
	if props&(1<<14) != 0 {
		m.Version = 1
		m.Phone = strings.TrimLeft(hex.EncodeToString(d[5:15]), "0")
		pos = 15
	} else {
		m.Phone = strings.TrimLeft(hex.EncodeToString(d[4:10]), "0")
	}
	m.Serial = int(binary.BigEndian.Uint16(d[pos:]))
	pos += 2
	if props&(1<<13) != 0 && len(d) >= pos+4 {
		m.Total = int(binary.BigEndian.Uint16(d[pos:]))
		m.Index = int(binary.BigEndian.Uint16(d[pos+2:]))
		pos += 4
	}
	end := pos + props&0x3FF
	if end > len(d) {
		end = len(d)
	}
	if pos > end {
		pos = end
	}
	m.Body = d[pos:end]
	return m
}

func (m *Msg) String() string {
	return fmt.Sprintf("%04X serial=%d len=%d", m.ID, m.Serial, len(m.Body))
}

// FrameReader 按 0x7e 切帧
type FrameReader struct{ buf []byte }

func (r *FrameReader) Feed(data []byte) []*Msg {
	r.buf = append(r.buf, data...)
	var out []*Msg
	for {
		s := bytes.IndexByte(r.buf, 0x7e)
		if s < 0 {
			r.buf = r.buf[:0]
			return out
		}
		e := bytes.IndexByte(r.buf[s+1:], 0x7e)
		if e < 0 {
			r.buf = r.buf[s:]
			return out
		}
		e += s + 1
		if e == s+1 { // 连续 7e：前一个是上一帧的尾
			r.buf = r.buf[e:]
			continue
		}
		if m := Parse(r.buf[s+1 : e]); m != nil {
			m.Raw = append([]byte(nil), r.buf[s:e+1]...)
			out = append(out, m)
		}
		r.buf = r.buf[e+1:]
	}
}

// ---------------- 消息体 ----------------

func bCommonReply(respSerial, respID, result int) []byte {
	return cat(u16(respSerial), u16(respID), []byte{byte(result)})
}

// 0x0100 终端注册
func bRegister(version int, plate, deviceID string) []byte {
	if version >= V2019 {
		return cat(u16(44), u16(300), fixed("SIMMK", 11), fixed("SIM-MODEL", 30), fixed(deviceID, 30), []byte{1}, gbk(plate))
	}
	return cat(u16(44), u16(300), fixed("SIMMK", 5), fixed("SIM-MODEL", 20), fixed(deviceID, 7), []byte{1}, gbk(plate))
}

// 0x0102 终端鉴权
func bAuth(version int, token string) []byte {
	if version >= V2019 {
		return cat(lstr(token), fixed("860000000000001", 15), fixed("SIM-1.0", 20))
	}
	return gbk(token)
}

type Loc struct {
	Lng, Lat, Speed float64
	Direction       int
}

// 0x0200 位置信息汇报：ACC 开 + 已定位，附带里程；extra 为已编码的附加信息
func bLocation(l Loc, extra []byte) []byte {
	return cat(u32(0), u32(0x3), u32(int(l.Lat*1e6)), u32(int(l.Lng*1e6)), u16(20), u16(int(l.Speed*10)),
		u16(l.Direction), bcdNow(), []byte{0x01, 4}, u32(12345), extra)
}

// aADAS 位置附加信息 0x64 ADAS 报警。flag = 终端ID+时间+序号+附件数+预留，平台会在 9208 中原样回传
func aADAS(version int, deviceID string, alarmID, typ, level, fileTotal int, l Loc) []byte {
	didLen, rsvLen := 7, 1
	if version >= V2019 {
		didLen, rsvLen = 30, 2
	}
	now := bcdNow()
	flag := cat(fixed(deviceID, didLen), now, []byte{0, byte(fileTotal)}, make([]byte, rsvLen))
	v := cat(u32(alarmID), []byte{1, byte(typ), byte(level), 30, 10, 0, 0, 0, byte(l.Speed)},
		u16(20), u32(int(l.Lat*1e6)), u32(int(l.Lng*1e6)), now, u16(0), flag)
	return cat([]byte{0x64, byte(len(v))}, v)
}

func bMediaHead(mediaID, typ, event, channel int) []byte {
	return cat(u32(mediaID), []byte{byte(typ), 0, byte(event), byte(channel)}, bLocation(Loc{113.264385, 23.129112, 40, 90}, nil)[:28])
}

func fakeJPEG(size int) []byte {
	head, _ := hex.DecodeString("FFD8FFE000104A46494600010100000100010000")
	n := size - len(head) - 2
	if n < 0 {
		n = 0
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return cat(head, b, []byte{0xff, 0xd9})
}

// 0x0107 查询终端属性应答
func bAttrReply(version int, deviceID string) []byte {
	did, model := 7, 20
	if version >= V2019 {
		did, model = 30, 30
	}
	iccid, _ := hex.DecodeString("89860000000000000001")
	return cat(u16(6), fixed("SIMMK", 5), fixed("SIM-MODEL", model), fixed(deviceID, did), iccid,
		lstr("HW1.0"), lstr("FW1.0"), []byte{3, 1})
}

// recorderFrame GB/T 19056 应答帧：55 7A 命令字 长度(2) 保留(1) 数据 校验
func recorderFrame(cmd byte, plate string) []byte {
	var data []byte
	switch cmd {
	case 0x05: // 车辆信息：VIN(17) + 车牌(12) + 车牌分类(12)
		data = cat(fixedPad("LSIM0000000000001", 17, ' '), fixed(plate, 12), fixed("大型货车", 12))
	case 0x01: // 驾驶人信息：机动车驾驶证号码(18)
		data = fixed("440100199001011234", 18)
	}
	raw := cat([]byte{0x55, 0x7a, cmd}, u16(len(data)), []byte{0}, data)
	return append(raw, bcc(raw))
}

// KV 命令行 k=v 参数
type KV map[string]string

func (kv KV) Int(k string, def int) int {
	if v, ok := kv[k]; ok {
		var n int
		if _, err := fmt.Sscan(v, &n); err == nil {
			return n
		}
	}
	return def
}

func (kv KV) Str(k, def string) string {
	if v, ok := kv[k]; ok {
		return v
	}
	return def
}

// bSimple 白名单内简单上行报文的消息体
func bSimple(version, msgID int, kv KV) ([]byte, error) {
	switch msgID {
	case 0x0108: // 升级结果
		return []byte{byte(kv.Int("type", 0)), byte(kv.Int("result", 0))}, nil
	case 0x0301: // 事件报告
		return []byte{byte(kv.Int("event_id", 1))}, nil
	case 0x0303: // 信息点播/取消
		return []byte{byte(kv.Int("type", 1)), byte(kv.Int("action", 1))}, nil
	case 0x0702: // 驾驶员身份信息
		status := kv.Int("status", 1)
		body := cat([]byte{byte(status)}, bcdNow())
		if status == 1 {
			card := kv.Int("card_status", 0)
			body = append(body, byte(card))
			if card == 0 {
				body = cat(body, lstr(kv.Str("name", "张三")), fixed(kv.Str("license_no", "440100000000000001"), 20),
					lstr("广州市交通运输局"), bcd("20301231", 4))
				if version >= V2019 {
					body = cat(body, fixed(kv.Str("id_card", "440100199001011234"), 20))
				}
			}
		}
		return body, nil
	case 0x0800: // 多媒体事件
		return cat(u32(kv.Int("media_id", 1)), []byte{byte(kv.Int("type", 0)), 0, byte(kv.Int("event", 0)),
			byte(kv.Int("channel", 1))}), nil
	case 0x0900: // 透传：类型 + 内容
		content := []byte("\x01\x02\x03HELLO")
		if n := kv.Int("size", 0); n > 0 {
			content = make([]byte, n)
			for i := range content {
				content[i] = byte(i)
			}
		}
		return cat([]byte{byte(kv.Int("type", 0xF0))}, content), nil
	case 0x0901: // 压缩上报：长度 + 内容
		data := append([]byte{0x1f, 0x8b}, make([]byte, 30)...)
		return cat(u32(len(data)), data), nil
	}
	return nil, fmt.Errorf("unsupported %04X", msgID)
}

// ---------------- 苏标附件（附件服务器） ----------------

type Info9208 struct {
	IP        string
	TCP, UDP  int
	Flag      []byte // 报警标识，原样回传
	AlarmNo   []byte // 32 字节报警编号
	FileTotal int
}

func parse9208(version int, b []byte) (*Info9208, error) {
	flagLen, rsv := 7+6+2+1, 1
	if version >= V2019 {
		flagLen, rsv = 30+6+2+2, 2
	}
	n := int(b[0])
	p := 5 + n
	if len(b) < p+flagLen+32 {
		return nil, fmt.Errorf("9208 body too short: %d", len(b))
	}
	i := &Info9208{IP: string(b[1 : 1+n]), TCP: int(binary.BigEndian.Uint16(b[1+n:])), UDP: int(binary.BigEndian.Uint16(b[3+n:]))}
	i.Flag = b[p : p+flagLen]
	i.AlarmNo = b[p+flagLen : p+flagLen+32]
	i.FileTotal = int(i.Flag[flagLen-1-rsv])
	return i, nil
}

type AttFile struct {
	Name string
	Type int
	Data []byte
}

// 0x1210 报警附件信息：终端ID + 报警标识 + 报警编号 BYTE[32] + 信息类型（0 正常 1 补传）+ 附件数量 + [文件名 文件大小]…
func b1210(version int, deviceID string, flag, alarmNo []byte, files []AttFile, infoType ...int) []byte {
	did := 7
	if version >= V2019 {
		did = 30
	}
	it := 0
	if len(infoType) > 0 {
		it = infoType[0]
	}
	body := cat(fixed(deviceID, did), flag, alarmNo, []byte{byte(it), byte(len(files))})
	for _, f := range files {
		body = cat(body, lstr(f.Name), u32(len(f.Data)))
	}
	return body
}

// 0x1211 文件信息上传 / 0x1212 文件上传完成（结构相同）
func b1211(name string, typ, size int) []byte { return cat(lstr(name), []byte{byte(typ)}, u32(size)) }

// dataPacket 文件数据包（码流，不转义）：0x30316364 + 50 字节文件名 + 偏移 + 长度 + 数据
func dataPacket(name string, offset int, data []byte) []byte {
	return cat([]byte{0x30, 0x31, 0x63, 0x64}, fixed(name, 50), u32(offset), u32(len(data)), data)
}
