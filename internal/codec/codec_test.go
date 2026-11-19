package codec

import (
	"bytes"
	"testing"
	"time"
)

// 组帧后能被自身解析、校验正确、手机号/流水号/消息体还原
func TestFrameRoundTrip(t *testing.T) {
	body := []byte{0x7e, 0x7d, 0x01, 0x02, 0x00, 0xff}
	for _, v := range []int{V2013, V2019} {
		f := Frame(0x0200, "13800000001", 0x7e7d, body, v, 0, 0)
		var r FrameReader
		ms := r.Feed(f)
		if len(ms) != 1 {
			t.Fatalf("version %d: %d 帧", v, len(ms))
		}
		m := ms[0]
		if !m.OK || m.Phone != "13800000001" || m.Version != v || m.Serial != 0x7e7d || !bytes.Equal(m.Body, body) || m.ID != 0x0200 {
			t.Fatalf("version %d: %+v", v, m)
		}
	}
}

func TestEscape(t *testing.T) {
	in := []byte{0x30, 0x7e, 0x08, 0x7d, 0x55}
	want := []byte{0x30, 0x7d, 0x02, 0x08, 0x7d, 0x01, 0x55}
	if got := Escape(in); !bytes.Equal(got, want) {
		t.Fatalf("Escape = % X", got)
	}
	if got := Unescape(want); !bytes.Equal(got, in) {
		t.Fatalf("Unescape = % X", got)
	}
	for _, bad := range [][]byte{{0x7d}, {0x7d, 0x03}, {0x01, 0x7d}} {
		if _, ok := UnescapeStrict(bad); ok {
			t.Fatalf("非法转义 % X 应失败", bad)
		}
	}
}

func TestBCD(t *testing.T) {
	if got := BCD("13800000001", 6); !bytes.Equal(got, []byte{0x01, 0x38, 0x00, 0x00, 0x00, 0x01}) {
		t.Fatalf("BCD = % X", got)
	}
	if got := BCD("12x4", 2); !bytes.Equal(got, []byte{0x12, 0x04}) {
		t.Fatalf("非数字按 0：% X", got)
	}
	if got := BCD("123456", 2); !bytes.Equal(got, []byte{0x34, 0x56}) {
		t.Fatalf("超长取低位：% X", got)
	}
	if len(BCD("1", 0)) != 0 || len(BCD("1", -1)) != 0 {
		t.Fatal("n<=0 应返回空")
	}
	ts := time.Date(2026, 11, 19, 8, 30, 5, 0, time.Local)
	r := NewReader(BCDTime(ts))
	if got := r.Time6(); !got.Equal(ts) {
		t.Fatalf("BCDTime 往返 %v", got)
	}
	if !bytes.Equal(BCDTime(time.Time{}), make([]byte, 6)) {
		t.Fatal("零值时间应为全 0")
	}
}

func TestStrings(t *testing.T) {
	if got := UnGBK(GBK("粤A12345")); got != "粤A12345" {
		t.Fatalf("GBK 往返 %q", got)
	}
	if got := Fixed("AB", 4); !bytes.Equal(got, []byte{'A', 'B', 0, 0}) {
		t.Fatalf("Fixed % X", got)
	}
	if got := LStr(string(bytes.Repeat([]byte{'x'}, 300))); got[0] != 255 || len(got) != 256 {
		t.Fatalf("LStr 截断 %d %d", got[0], len(got))
	}
	if BCC([]byte{1, 2, 4}) != 7 {
		t.Fatal("BCC")
	}
}

// 畸形帧不得 panic：截断的 2019 头、截断的分包头
func TestParseMalformed(t *testing.T) {
	short := []byte{0x02, 0x00, 0x40, 0x00, 0x01, 1, 2, 3, 4, 5, 6, 7, 8}
	if m := Parse(short); m != nil {
		t.Fatalf("截断 2019 头应返回 nil: %+v", m)
	}
	truncSub := []byte{0x02, 0x00, 0x20, 0x00, 1, 2, 3, 4, 5, 6, 0, 1, 0}
	if m := Parse(truncSub); m != nil {
		t.Fatalf("截断分包头应返回 nil: %+v", m)
	}
	for n := 0; n < 24; n++ {
		Parse(make([]byte, n))
	}
	// 消息体长度与属性不符：OK=false
	f := Frame(0x8001, "1", 1, []byte{1, 2, 3}, V2013, 0, 0)
	raw := Unescape(f[1 : len(f)-1])
	raw[3] = 9 // 声明 9 字节
	raw[len(raw)-1] = BCC(raw[:len(raw)-1])
	if m := Parse(Escape(raw)); m == nil || m.OK {
		t.Fatalf("长度不符应 OK=false: %+v", m)
	}
}

// 切帧：粘包、半包、连续 7e、坏帧
func TestFrameReader(t *testing.T) {
	a := Frame(0x8001, "13800000001", 1, []byte{0, 1, 2, 0, 0}, V2019, 0, 0)
	b := Frame(0x8103, "13800000001", 2, []byte{0}, V2019, 0, 0)
	var r FrameReader
	stream := append(append(append([]byte{}, a...), b...), 0x7e, 0x7d, 0x7e)
	ms, bad := r.FeedWithBad(stream[:5])
	if len(ms) != 0 || len(bad) != 0 {
		t.Fatal("半包不应出帧")
	}
	ms, bad = r.FeedWithBad(stream[5:])
	if len(ms) != 2 || ms[0].ID != 0x8001 || ms[1].ID != 0x8103 || len(bad) != 1 {
		t.Fatalf("粘包 %d 帧 坏帧 %d", len(ms), len(bad))
	}
	if !bytes.Equal(ms[0].Raw, a) {
		t.Fatal("Raw 应为原始帧")
	}
}

func TestAssembler(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 30)
	parts := [][]byte{body[:100], body[100:200], body[200:]}
	a := Assembler{}
	now := time.Now()
	// 乱序到达：3、1、2
	for k, i := range []int{3, 1} {
		m := &Msg{ID: 0x8103, Serial: 10 + i - 1, Total: 3, Index: i, Body: parts[i-1]}
		full, err := a.Add(m, now)
		if err != nil || full != nil {
			t.Fatalf("第 %d 包不应收齐: %v %v", k, full, err)
		}
	}
	miss := a[0x8103].Missing(0)
	if len(miss) != 1 || miss[0] != 2 {
		t.Fatalf("缺失包 %v", miss)
	}
	req, _ := a[0x8103].ResendRequest()
	if !bytes.Equal(req, []byte{0, 10, 1, 0, 2}) {
		t.Fatalf("0005 消息体 % X", req)
	}
	full, err := a.Add(&Msg{ID: 0x8103, Serial: 11, Total: 3, Index: 2, Body: parts[1]}, now)
	if err != nil || full == nil || !bytes.Equal(full.Body, body) || full.Total != 0 || full.Index != 0 {
		t.Fatalf("重组失败 %+v %v", full, err)
	}
	if len(a) != 0 {
		t.Fatal("收齐后应移除")
	}
	for _, m := range []*Msg{{Total: 0, Index: 1}, {Total: 2, Index: 0}, {Total: 2, Index: 3}} {
		if _, err := a.Add(m, now); err == nil {
			t.Fatalf("非法分包序号应报错 %+v", m)
		}
	}
}

func TestReader(t *testing.T) {
	r := NewReader([]byte{1, 0, 2, 0, 0, 0, 3, 2, 'h', 'i'})
	if r.U8() != 1 || r.U16() != 2 || r.U32() != 3 || r.LStr() != "hi" || r.Err != nil || r.Left() != 0 {
		t.Fatal("顺序读取错误")
	}
	if r.U16() != 0 || r.Err == nil {
		t.Fatal("越界应置 Err 并返回 0")
	}
	if got := r.Take(-1); len(got) != 0 {
		t.Fatal("负长度应返回空")
	}
	if got := NewReader([]byte{1, 2, 3}).Slice(1, 9); !bytes.Equal(got, []byte{2, 3}) {
		t.Fatalf("Slice 裁剪 % X", got)
	}
}
