package codec

// 下行消息体顺序读取；越界时记录错误并返回零值，处理完后检查 Err

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Reader 消息体读取器
type Reader struct {
	b   []byte
	p   int
	Err error
}

// NewReader 从消息体创建读取器
func NewReader(b []byte) *Reader { return &Reader{b: b} }

// Take 读取 n 字节；越界时置 Err 并返回 n 字节的 0
func (r *Reader) Take(n int) []byte {
	if r.Err != nil || n < 0 || r.p+n > len(r.b) {
		if r.Err == nil {
			r.Err = fmt.Errorf("消息体过短：需要 %d 字节，剩余 %d", n, len(r.b)-r.p)
		}
		return make([]byte, max(n, 0))
	}
	v := r.b[r.p : r.p+n]
	r.p += n
	return v
}

func (r *Reader) U8() int  { return int(r.Take(1)[0]) }
func (r *Reader) U16() int { return int(binary.BigEndian.Uint16(r.Take(2))) }
func (r *Reader) U32() int { return int(binary.BigEndian.Uint32(r.Take(4))) }

// Left 剩余字节数
func (r *Reader) Left() int { return len(r.b) - r.p }

// Rest 读取剩余全部字节
func (r *Reader) Rest() []byte { return r.Take(r.Left()) }

// Str n 字节 GBK 字符串（去掉尾部 0）
func (r *Reader) Str(n int) string { return strings.TrimRight(UnGBK(r.Take(n)), "\x00") }

// LStr 1 字节长度前缀字符串；WStr 2 字节长度前缀
func (r *Reader) LStr() string { return r.Str(r.U8()) }
func (r *Reader) WStr() string { return r.Str(r.U16()) }

// BCD n 字节 BCD 转十六进制串
func (r *Reader) BCD(n int) string { return hex.EncodeToString(r.Take(n)) }

// Time6 BCD YYMMDDHHMMSS；全 0 或非法返回零值
func (r *Reader) Time6() time.Time {
	s := r.BCD(6)
	t, err := time.ParseInLocation("060102150405", s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Pos 当前读取位置
func (r *Reader) Pos() int { return r.p }

// Slice 消息体中 [from, to) 的字节（越界时裁剪）
func (r *Reader) Slice(from, to int) []byte {
	from, to = max(0, min(from, len(r.b))), max(0, min(to, len(r.b)))
	if from > to {
		return nil
	}
	return r.b[from:to]
}
