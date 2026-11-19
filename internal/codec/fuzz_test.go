package codec

// 模糊测试：任意下行字节不得 panic，且编解码往返一致。
// 运行：go test ./internal/codec -run '^$' -fuzz FuzzParse -fuzztime 30s

import (
	"bytes"
	"testing"
	"time"
)

func FuzzParse(f *testing.F) {
	f.Add(Frame(0x0200, "13800000001", 1, []byte{0, 0, 0, 0}, V2019, 0, 0))
	f.Add(Frame(0x8103, "13800000001", 7, []byte{1, 0, 0, 0, 0x55, 1, 60}, V2013, 2, 1))
	f.Add([]byte{0x7e, 0x02, 0x00, 0x40, 0x00, 0x01, 0x7d, 0x02, 0x7e})
	f.Add([]byte{0x7e, 0x7e, 0x7d, 0x7e})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r FrameReader
		for _, m := range r.Feed(data) {
			_ = m.String()
			if m.Total > 0 {
				_ = CheckPart(m)
			}
		}
		if m := Parse(data); m != nil && len(m.Body) > 0x3FF {
			t.Fatalf("消息体超过 10 位长度上限: %d", len(m.Body))
		}
	})
}

// 反转义：任意输入不 panic；转义后再反转义得到原文
func FuzzUnescape(f *testing.F) {
	f.Add([]byte{0x7d, 0x01, 0x7d, 0x02})
	f.Add([]byte{0x7e, 0x7d, 0x00})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		UnescapeStrict(data)
		esc := Escape(data)
		if bytes.IndexByte(esc, 0x7e) >= 0 {
			t.Fatal("转义结果不应含 0x7e")
		}
		got, ok := UnescapeStrict(esc)
		if !ok || !bytes.Equal(got, data) {
			t.Fatalf("往返不一致: % X -> % X", data, got)
		}
	})
}

// 组帧往返：任意消息 ID / 手机号 / 流水号 / 消息体（≤1023 字节）组帧后能原样解析
func FuzzFrameRoundTrip(f *testing.F) {
	f.Add(uint16(0x0200), "13800000001", uint16(1), []byte{1, 2, 3}, true, uint16(0), uint16(0))
	f.Add(uint16(0x8103), "1", uint16(0xFFFF), []byte{0x7e, 0x7d}, false, uint16(3), uint16(2))
	f.Fuzz(func(t *testing.T, id uint16, phone string, serial uint16, body []byte, v19 bool, total, index uint16) {
		if len(body) > 0x3FF {
			body = body[:0x3FF]
		}
		ver := V2013
		if v19 {
			ver = V2019
		}
		fr := Frame(int(id), phone, int(serial), body, ver, int(total), int(index))
		var r FrameReader
		ms := r.Feed(fr)
		if len(ms) != 1 {
			t.Fatalf("应解析出 1 帧，得到 %d", len(ms))
		}
		m := ms[0]
		if !m.OK || m.ID != int(id) || m.Serial != int(serial) || !bytes.Equal(m.Body, body) || m.Version != ver {
			t.Fatalf("往返不一致: %+v", m)
		}
		if total > 0 && (m.Total != int(total) || m.Index != int(index)) {
			t.Fatalf("分包字段不一致: %d/%d", m.Index, m.Total)
		}
	})
}

// 分包重组：任意分包序列不 panic；收齐后的消息体等于按序号拼接
func FuzzAssembler(f *testing.F) {
	f.Add([]byte{3, 1, 3, 2, 2, 3}, []byte("abcdef"))
	f.Add([]byte{0, 0, 255, 255}, []byte{})
	f.Fuzz(func(t *testing.T, seq []byte, payload []byte) {
		a := Assembler{}
		now := time.Unix(0, 0)
		for i := 0; i+1 < len(seq); i += 2 {
			total, index := int(seq[i]%8), int(seq[i+1]%9)
			m := &Msg{ID: 0x8103 + int(seq[i]%2), Serial: i, Total: total, Index: index, Body: payload}
			full, err := a.Add(m, now)
			if err != nil {
				if CheckPart(m) == nil {
					t.Fatalf("合法分包报错: %v", err)
				}
				continue
			}
			if full != nil && len(full.Body) != total*len(payload) {
				t.Fatalf("重组长度 %d，期望 %d", len(full.Body), total*len(payload))
			}
		}
		for _, p := range a {
			body, miss := p.ResendRequest()
			if len(body) != 3+2*len(miss) || len(miss) > 255 {
				t.Fatalf("0005 消息体长度错误")
			}
		}
	})
}

// 消息体读取器：任意读取序列不 panic，越界后 Err 不再被清除
func FuzzReader(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8}, []byte{0, 1, 2, 3, 4, 5})
	f.Fuzz(func(t *testing.T, body []byte, ops []byte) {
		r := NewReader(body)
		failed := false
		for _, op := range ops {
			switch op % 8 {
			case 0:
				r.U8()
			case 1:
				r.U16()
			case 2:
				r.U32()
			case 3:
				r.LStr()
			case 4:
				r.WStr()
			case 5:
				r.Time6()
			case 6:
				r.Take(int(op) - 128)
			case 7:
				r.Rest()
			}
			if failed && r.Err == nil {
				t.Fatal("Err 被清除")
			}
			failed = r.Err != nil
			if r.Left() < 0 {
				t.Fatal("剩余字节为负")
			}
		}
	})
}
