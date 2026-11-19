package main

// 下行消息体顺序读取：实现在 internal/codec（codec.Reader），这里是包内小写方法的薄封装；
// 越界时记录错误并返回零值，处理完后检查 r.err

import (
	"time"

	"jt808sim/internal/codec"
)

type reader struct {
	c   *codec.Reader
	err error
}

func newReader(b []byte) *reader { return &reader{c: codec.NewReader(b)} }

// sync 把底层读取器的错误同步到 r.err（保持旧代码 r.err != nil 的写法）
func (r *reader) sync() { r.err = r.c.Err }

func (r *reader) take(n int) []byte { defer r.sync(); return r.c.Take(n) }
func (r *reader) u8() int           { defer r.sync(); return r.c.U8() }
func (r *reader) u16() int          { defer r.sync(); return r.c.U16() }
func (r *reader) u32() int          { defer r.sync(); return r.c.U32() }
func (r *reader) left() int         { return r.c.Left() }
func (r *reader) rest() []byte      { defer r.sync(); return r.c.Rest() }

// str n 字节 GBK 字符串（去掉尾部 0）
func (r *reader) str(n int) string { defer r.sync(); return r.c.Str(n) }

// lstr 1 字节长度前缀；wstr 2 字节长度前缀
func (r *reader) lstr() string { defer r.sync(); return r.c.LStr() }
func (r *reader) wstr() string { defer r.sync(); return r.c.WStr() }

func (r *reader) bcd(n int) string { defer r.sync(); return r.c.BCD(n) }

// time6 BCD YYMMDDHHMMSS；全 0 返回零值
func (r *reader) time6() time.Time { defer r.sync(); return r.c.Time6() }

func bcdTime(t time.Time) []byte { return codec.BCDTime(t) }
