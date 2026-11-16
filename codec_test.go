package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// 编解码自检：组帧后能被自身解析、校验正确、手机号还原
func TestRoundTrip(t *testing.T) {
	for _, v := range []int{V2013, V2019} {
		f := Frame(0x0200, "13800000001", 1, bLocation(Loc{113.26, 23.12, 40, 90}, nil), v, 0, 0)
		var r FrameReader
		ms := r.Feed(f)
		if len(ms) != 1 || !ms[0].OK || ms[0].Phone != "13800000001" || ms[0].Version != v {
			t.Fatalf("version %d: %+v", v, ms)
		}
	}
}

// 畸形帧不得 panic：截断的 2019 头、截断的分包头、空 body 的 9208
func TestParseMalformed(t *testing.T) {
	// 13 字节（过长度门槛）但置了版本位：2019 头至少需要 18 字节（含校验）
	short := []byte{0x02, 0x00, 0x40, 0x00, 0x01, 1, 2, 3, 4, 5, 6, 7, 8}
	if m := Parse(short); m != nil {
		t.Fatalf("截断 2019 头应返回 nil: %+v", m)
	}
	// 2013 头声明分包但总包数/序号被截断（13 字节只够到流水号）
	truncSub := []byte{0x02, 0x00, 0x20, 0x00, 1, 2, 3, 4, 5, 6, 0, 1, 0}
	if m := Parse(truncSub); m != nil {
		t.Fatalf("截断分包头应返回 nil: %+v", m)
	}
	// 随机短切片不 panic
	for n := 0; n < 24; n++ {
		Parse(make([]byte, n))
	}
	if _, err := parse9208(V2019, nil); err == nil {
		t.Fatal("空 9208 body 应报错")
	}
}

// fuzz Parse + FrameReader：任意字节序列不得 panic
func FuzzParse(f *testing.F) {
	f.Add(Frame(0x0200, "13800000001", 1, bLocation(Loc{113.26, 23.12, 40, 90}, nil), V2019, 0, 0))
	f.Add([]byte{0x7e, 0x02, 0x00, 0x40, 0x00, 0x01, 0x7d, 0x02, 0x7e})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r FrameReader
		for _, m := range r.Feed(data) {
			_ = m.String()
		}
		Parse(data)
	})
}

// 设置 JT808_DUMP=文件路径 时导出各类上行帧（hex），用于与网关 Java 解码器交叉验证
func TestDump(t *testing.T) {
	path := os.Getenv("JT808_DUMP")
	if path == "" {
		t.Skip("JT808_DUMP not set")
	}
	var out []string
	for _, v := range []int{V2013, V2019} {
		add := func(id int, body []byte) {
			f := Frame(id, "13800000001", 7, body, v, 0, 0)
			out = append(out, fmt.Sprintf("%d %s", v, hex.EncodeToString(f[1:len(f)-1])))
		}
		l := Loc{113.264385, 23.129112, 40, 90}
		add(0x0100, bRegister(v, "粤B12345", "SIM0001"))
		add(0x0102, bAuth(v, "ABC123"))
		add(0x0200, bLocation(l, aADAS(v, "SIM0001", 1, 1, 1, 3, l)))
		for _, s := range []struct {
			id int
			kv KV
		}{{0x0108, KV{}}, {0x0301, KV{}}, {0x0303, KV{}}, {0x0702, KV{}}, {0x0702, KV{"status": "2"}},
			{0x0800, KV{}}, {0x0900, KV{}}, {0x0901, KV{}}} {
			b, _ := bSimple(v, s.id, s.kv)
			add(s.id, b)
		}
		add(0x0801, cat(bMediaHead(5, 0, 0, 1), fakeJPEG(200)))
		add(0x0201, cat(u16(9), bLocation(l, nil)))
		add(0x0107, bAttrReply(v, "SIM0001"))
		add(0x0700, cat(u16(9), []byte{5}, recorderFrame(5, "粤B12345")))
		flag := aADAS(v, "SIM0001", 1, 1, 1, 3, l)
		flag = flag[len(flag)-map[int]int{V2013: 16, V2019: 40}[v]:]
		add(0x1210, b1210(v, "SIM0001", flag, fixed("2001", 32), []AttFile{{"00_65_6501_0_2001.jpg", 0, make([]byte, 1500)}}))
		add(0x1211, b1211("00_65_6501_0_2001.jpg", 0, 1500))
		add(0x1212, b1211("00_65_6501_0_2001.jpg", 0, 1500))
	}
	os.WriteFile(path, []byte(strings.Join(out, "\n")), 0644)
}

// 设置 JT808_DOWN=文件路径 时解析网关 Java 编码出的下行帧
func TestDown(t *testing.T) {
	path := os.Getenv("JT808_DOWN")
	if path == "" {
		t.Skip("JT808_DOWN not set")
	}
	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		p := strings.Fields(line)
		b, _ := hex.DecodeString(p[2])
		var r FrameReader
		m := r.Feed(b)[0]
		if !m.OK || m.Phone != "13800000001" {
			t.Fatalf("%s: ok=%v phone=%s", p[0], m.OK, m.Phone)
		}
		switch p[0] {
		case "8100":
			t.Logf("8100 v%d result=%d token=%s", m.Version, m.Body[2], ungbk(m.Body[3:]))
		case "8001":
			t.Logf("8001 v%d result=%d", m.Version, m.Body[4])
		case "9208":
			i, err := parse9208(m.Version, m.Body)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("9208 v%d %s:%d fileTotal=%d alarmNo=%s", m.Version, i.IP, i.TCP, i.FileTotal, strings.TrimRight(string(i.AlarmNo), "\x00"))
		case "9212":
			k := int(m.Body[0])
			t.Logf("9212 v%d %s result=%d", m.Version, m.Body[1:1+k], m.Body[k+2])
		}
	}
}

// 重传超时 T(n+1) = T(n) × (n+1)：T0、T0、2T0、6T0、24T0
func TestRetransTimeout(t *testing.T) {
	want := []time.Duration{1, 1, 2, 6, 24, 120}
	for try, w := range want {
		if got := retransTimeout(time.Second, try); got != w*time.Second {
			t.Fatalf("try=%d got %v want %v", try, got, w*time.Second)
		}
	}
	if retransTimeout(time.Hour, 30) != 24*time.Hour {
		t.Fatal("超大重传次数应封顶")
	}
}

// 8108 制造商ID：2013 BYTE[5]，2019 BYTE[11]；长度对不上时回退另一种
func TestParse8108(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5}
	b2019 := cat([]byte{0}, fixed("MAKER123456", 11), lstr("FW2.0"), u32(len(data)), data)
	b2013 := cat([]byte{0}, fixed("MK001", 5), lstr("FW2.0"), u32(len(data)), data)
	for _, c := range []struct {
		ver      int
		body     []byte
		maker    string
		makerLen int
	}{{V2019, b2019, "MAKER123456", 11}, {V2013, b2013, "MK001", 5}, {V2019, b2013, "MK001", 5}, {V2013, b2019, "MAKER123456", 11}} {
		u, err := parse8108(c.ver, c.body)
		if err != nil || u.maker != c.maker || u.makerLen != c.makerLen || u.ver != "FW2.0" || !bytes.Equal(u.data, data) {
			t.Fatalf("ver=%d %+v %v", c.ver, u, err)
		}
	}
	if _, err := parse8108(V2019, b2019[:len(b2019)-1]); err == nil {
		t.Fatal("数据不全应报错")
	}
}

// 8500：2013 控制标志；2019 控制类型列表（0x0001 车门：0 锁闭 1 开启）；网关旧 4 字节 / jt808-server 3 字节格式；不支持的类型
func TestParse8500(t *testing.T) {
	for _, c := range []struct {
		ver         int
		body        []byte
		lock        bool
		legacy, bad bool
		unsupported bool
	}{
		{V2013, []byte{1}, true, false, false, false},
		{V2013, []byte{0}, false, false, false, false},
		{V2019, cat(u16(1), u16(1), []byte{0}), true, false, false, false},
		{V2019, cat(u16(1), u16(1), []byte{1}), false, false, false, false},
		{V2019, cat(u16(2), u16(1), []byte{1}, u16(1), []byte{0}), true, false, false, false},
		{V2019, []byte{1, 0, 1, 1}, true, true, false, false},
		{V2019, cat(u16(1), u16(0xF001), []byte{5}), false, false, false, true},
		{V2019, u16(1), false, false, true, false},
		{V2019, cat(u16(1), []byte{1}), true, true, false, false}, // 3 字节 = jt808-server 格式（类型ID + 参数 1=加锁）
		{V2019, u16(0), false, false, true, false},
	} {
		r, err := parse8500(c.ver, c.body)
		if c.bad {
			if err == nil {
				t.Fatalf("%X 应解析失败", c.body)
			}
			continue
		}
		if err != nil || r.lock != c.lock || r.legacy != c.legacy || (len(r.unsupported) > 0) != c.unsupported {
			t.Fatalf("%X: %+v %v", c.body, r, err)
		}
	}
}

// 结构体参数：默认值逐字段解析后再编码应还原；字段值校验
func TestParamSchemas(t *testing.T) {
	for _, d := range paramDefs {
		if d.Schema == nil {
			continue
		}
		raw := encodeValue(d.ID, d.Def)
		f := d.Schema.decode(raw)
		v, err := encodeParamFields(d.ID, f)
		if err != nil {
			t.Fatalf("%04X: %v", d.ID, err)
		}
		if v != d.Def {
			t.Fatalf("%04X 往返不一致 %s -> %s", d.ID, d.Def, v)
		}
	}
	sizes := map[int]int{0x0075: 21, 0x0032: 4, 0x0110: 8, 0x007C: 20, 0x0079: 3, 0x007B: 2, 0xF364: 56, 0xF365: 65, 0xF366: 36, 0xF367: 2}
	for id, n := range sizes {
		if got := len(encodeValue(id, paramIndex[id].Def)); got != n {
			t.Fatalf("%04X 默认值 %d 字节，应为 %d", id, got, n)
		}
	}
	// 0x0076：通道计数按通道类型自动统计
	v, err := encodeParamFields(0x0076, map[string]any{"items": []any{
		map[string]any{"phys": 1.0, "logic": 1.0, "type": 0.0, "ptz": 1.0},
		map[string]any{"phys": 2.0, "logic": 2.0, "type": 2.0, "ptz": 0.0},
		map[string]any{"phys": 3.0, "logic": 3.0, "type": 1.0, "ptz": 0.0}}})
	if err != nil || v != "010101"+"01010001"+"02020200"+"03030100" {
		t.Fatalf("0076 %s %v", v, err)
	}
	// 0x0077：单独通道参数，数量自动
	v, err = encodeParamFields(0x0077, map[string]any{"items": []any{map[string]any{"logic": 2.0, "rtFps": 25.0}}})
	if err != nil || len(v) != 2*(1+21) || v[:4] != "0102" {
		t.Fatalf("0077 %s %v", v, err)
	}
	// 0x0064 位段：通道1/2 开、通道1 上传、分钟、间隔 3
	v, err = encodeParamFields(0x0064, map[string]any{"enable": 3.0, "upload": 1.0, "unit": 1.0, "interval": 3.0})
	if err != nil || v != itoa(3|1<<8|1<<16|3<<17) {
		t.Fatalf("0064 %s %v", v, err)
	}
	// 0x0110 CAN：CAN2 扩展帧 ID 0x18FEF100 间隔 100ms
	v, err = encodeParamFields(0x0150, map[string]any{"interval": 100.0, "channel": 1.0, "frame": 1.0, "method": 0.0, "canId": "0x18FEF100"})
	if err != nil || v != "00000064"+"d8fef100" {
		t.Fatalf("0150 %s %v", v, err)
	}
	if _, err := encodeParamFields(0x0032, map[string]any{"sh": 24.0}); err == nil {
		t.Fatal("0032 小时 24 应校验失败")
	}
	if _, err := validateParam(0x0032, "0102"); err == nil {
		t.Fatal("0032 长度不足应校验失败")
	}
	if !inPeriod("16320a1e", time.Date(2026, 1, 1, 23, 0, 0, 0, time.Local)) || inPeriod("16320a1e", time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)) ||
		!inPeriod("16320a1e", time.Date(2026, 1, 1, 9, 0, 0, 0, time.Local)) || inPeriod("00000000", time.Now()) {
		t.Fatal("0032 违规时段判断错误")
	}
	// TPMS 字符串字段
	f := paramFields(0xF366, paramIndex[0xF366].Def)
	if f["tire"] != "900R20" || f["unit"] != uint64(3) {
		t.Fatalf("F366 %v", f)
	}
}

// RSA 分组加解密：117 字节明文一组，128 字节密文一组
func TestRSABlocks(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 1024)
	plain := bytes.Repeat([]byte("0123456789"), 30) // 300 字节 -> 3 组
	c, err := rsaEncrypt(&k.PublicKey, plain)
	if err != nil || len(c) != 3*128 {
		t.Fatalf("密文 %d 字节 %v", len(c), err)
	}
	p, err := rsaDecrypt(k, c)
	if err != nil || !bytes.Equal(p, plain) {
		t.Fatalf("解密 %v", err)
	}
	if _, err := rsaDecrypt(k, c[:100]); err == nil {
		t.Fatal("非整组密文应报错")
	}
}
