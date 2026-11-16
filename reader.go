package main

// 下行消息体顺序读取；越界时记录错误并返回零值，处理完后检查 r.err

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type reader struct {
	b   []byte
	p   int
	err error
}

func newReader(b []byte) *reader { return &reader{b: b} }

func (r *reader) take(n int) []byte {
	if r.err != nil || n < 0 || r.p+n > len(r.b) {
		if r.err == nil {
			r.err = fmt.Errorf("消息体过短：需要 %d 字节，剩余 %d", n, len(r.b)-r.p)
		}
		return make([]byte, max(n, 0))
	}
	v := r.b[r.p : r.p+n]
	r.p += n
	return v
}

func (r *reader) u8() int  { return int(r.take(1)[0]) }
func (r *reader) u16() int { return int(binary.BigEndian.Uint16(r.take(2))) }
func (r *reader) u32() int { return int(binary.BigEndian.Uint32(r.take(4))) }
func (r *reader) left() int {
	return len(r.b) - r.p
}
func (r *reader) rest() []byte { return r.take(r.left()) }

// str n 字节 GBK 字符串（去掉尾部 0）
func (r *reader) str(n int) string { return strings.TrimRight(ungbk(r.take(n)), "\x00") }

// lstr 1 字节长度前缀；wstr 2 字节长度前缀
func (r *reader) lstr() string { return r.str(r.u8()) }
func (r *reader) wstr() string { return r.str(r.u16()) }

func (r *reader) bcd(n int) string { return hex.EncodeToString(r.take(n)) }

// time6 BCD YYMMDDHHMMSS；全 0 返回零值
func (r *reader) time6() time.Time {
	s := r.bcd(6)
	t, err := time.ParseInLocation("060102150405", s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

func bcdTime(t time.Time) []byte {
	if t.IsZero() {
		return make([]byte, 6)
	}
	return bcd(t.In(time.Local).Format("060102150405"), 6)
}
