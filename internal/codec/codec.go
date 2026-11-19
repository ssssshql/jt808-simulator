// Package codec JT/T 808 协议帧编解码：转义 / 反转义、BCC 校验、消息头（2013 / 2019 双版本）、
// 分包标志、BCD、GBK 字符串、按 0x7e 切帧，以及下行分包重组与消息体顺序读取。
// 只依赖标准库与 x/text，不涉及终端状态，便于单独测试与模糊测试。
package codec

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

// GBK 字符串编码为 GBK；无法编码时原样返回 UTF-8 字节
func GBK(s string) []byte {
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return []byte(s)
	}
	return b
}

// UnGBK GBK 字节解码为字符串；解码失败时按原字节返回
func UnGBK(b []byte) string {
	s, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(s)
}

// BCD 数字串 -> 定长 BCD（左补 0，非十六进制字符按 0 处理，确保返回精确 n 字节）
func BCD(s string, n int) []byte {
	if n <= 0 {
		return []byte{}
	}
	var clean strings.Builder
	clean.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			clean.WriteByte(c)
		} else {
			clean.WriteByte('0')
		}
	}
	s = clean.String()
	if len(s) < n*2 {
		s = strings.Repeat("0", n*2-len(s)) + s
	}
	b, err := hex.DecodeString(s[len(s)-n*2:])
	if err != nil || len(b) != n {
		return make([]byte, n)
	}
	return b
}

// BCDNow 当前本地时间 YYMMDDhhmmss 的 6 字节 BCD
func BCDNow() []byte { return BCD(time.Now().Format("060102150405"), 6) }

// BCDTime 指定时间的 6 字节 BCD（零值返回全 0）
func BCDTime(t time.Time) []byte {
	if t.IsZero() {
		return make([]byte, 6)
	}
	return BCD(t.In(time.Local).Format("060102150405"), 6)
}

// Fixed 定长字符串，右补 0x00
func Fixed(s string, n int) []byte { return FixedPad(s, n, 0) }

// FixedPad 定长 GBK 字符串，超长截断、不足用 pad 右补
func FixedPad(s string, n int, pad byte) []byte {
	b := GBK(s)
	if len(b) > n {
		b = b[:n]
	}
	return append(b, bytes.Repeat([]byte{pad}, n-len(b))...)
}

// LStr 1 字节长度前缀字符串（超过 255 字节截断，避免长度溢出回绕）
func LStr(s string) []byte {
	b := GBK(s)
	if len(b) > 255 {
		b = b[:255]
	}
	return append([]byte{byte(len(b))}, b...)
}

// BCC 异或校验
func BCC(b []byte) byte {
	var c byte
	for _, x := range b {
		c ^= x
	}
	return c
}

// Escape 0x7e -> 0x7d 0x02，0x7d -> 0x7d 0x01
func Escape(b []byte) []byte {
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

// UnescapeStrict 反转义；出现非法转义序列（0x7d 后不是 01/02 或位于末尾）返回 false
func UnescapeStrict(b []byte) ([]byte, bool) {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == 0x7d {
			if i+1 >= len(b) {
				return nil, false
			}
			switch b[i+1] {
			case 0x01:
				out = append(out, 0x7d)
			case 0x02:
				out = append(out, 0x7e)
			default:
				return nil, false
			}
			i++
		} else {
			out = append(out, b[i])
		}
	}
	return out, true
}

// Unescape 同 UnescapeStrict，非法时返回 nil
func Unescape(b []byte) []byte {
	out, ok := UnescapeStrict(b)
	if !ok {
		return nil
	}
	return out
}

// U16 大端 WORD
func U16(v int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(v)) }

// U32 大端 DWORD
func U32(v int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }

// Cat 拼接
func Cat(parts ...[]byte) []byte {
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
		head = Cat(U16(msgID), U16(props), []byte{1}, BCD(phone, 10))
	} else {
		head = Cat(U16(msgID), U16(props), BCD(phone, 6))
	}
	head = append(head, U16(serial)...)
	if total > 0 {
		head = Cat(head, U16(total), U16(index))
	}
	raw := Cat(head, body)
	raw = append(raw, BCC(raw))
	return Cat([]byte{0x7e}, Escape(raw), []byte{0x7e})
}

// Msg 解析后的一帧
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

// Parse raw 为去掉首尾 0x7e 的转义数据；格式错误（转义非法、头被截断）返回 nil，
// 校验码错误或消息体长度与属性不符时返回 OK=false 的 Msg
func Parse(raw []byte) *Msg {
	d, ok := UnescapeStrict(raw)
	if !ok || len(d) < 13 {
		return nil
	}
	m := &Msg{OK: BCC(d[:len(d)-1]) == d[len(d)-1]}
	d = d[:len(d)-1]
	m.ID = int(binary.BigEndian.Uint16(d))
	props := int(binary.BigEndian.Uint16(d[2:]))
	m.RSA = props&(1<<10) != 0
	pos := 10
	if props&(1<<14) != 0 {
		if len(d) < 17 { // 2019 头被截断：版本(1)+手机号(10)+流水号(2)
			return nil
		}
		m.Version = 1
		m.Phone = strings.TrimLeft(hex.EncodeToString(d[5:15]), "0")
		pos = 15
	} else {
		m.Phone = strings.TrimLeft(hex.EncodeToString(d[4:10]), "0")
	}
	if len(d) < pos+2 {
		return nil
	}
	m.Serial = int(binary.BigEndian.Uint16(d[pos:]))
	pos += 2
	if props&(1<<13) != 0 {
		if len(d) < pos+4 { // 声明了分包但总包数/序号被截断
			return nil
		}
		m.Total = int(binary.BigEndian.Uint16(d[pos:]))
		m.Index = int(binary.BigEndian.Uint16(d[pos+2:]))
		pos += 4
	}
	bodyLen := props & 0x3FF
	end := pos + bodyLen
	if end != len(d) {
		m.OK = false
		if end > len(d) {
			end = len(d)
		}
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

// MaxFrameBuf 半帧（只有起始 0x7e、没有结尾）缓冲上限，超过即丢弃，防对端撑爆内存
const MaxFrameBuf = 1 << 20

// Feed 喂入数据，返回其中完整且可解析的帧
func (r *FrameReader) Feed(data []byte) []*Msg {
	out, _ := r.FeedWithBad(data)
	return out
}

// FeedWithBad 同 Feed，另外返回无法解析的原始帧（含首尾 0x7e）
func (r *FrameReader) FeedWithBad(data []byte) ([]*Msg, [][]byte) {
	r.buf = append(r.buf, data...)
	var out []*Msg
	var bad [][]byte
	for {
		s := bytes.IndexByte(r.buf, 0x7e)
		if s < 0 {
			r.buf = r.buf[:0]
			return out, bad
		}
		e := bytes.IndexByte(r.buf[s+1:], 0x7e)
		if e < 0 {
			r.buf = r.buf[s:]
			if len(r.buf) > MaxFrameBuf {
				r.buf = r.buf[:0]
			}
			return out, bad
		}
		e += s + 1
		if e == s+1 { // 连续 7e：前一个是上一帧的尾
			r.buf = r.buf[e:]
			continue
		}
		raw := append([]byte(nil), r.buf[s:e+1]...)
		if m := Parse(r.buf[s+1 : e]); m != nil {
			m.Raw = raw
			out = append(out, m)
		} else {
			bad = append(bad, raw)
		}
		r.buf = r.buf[e+1:]
	}
}
