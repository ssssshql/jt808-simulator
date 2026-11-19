package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"
)

func TestLoginAndParams(t *testing.T) {
	for _, ver := range []string{"2013", "2019"} {
		t.Run(ver, func(t *testing.T) {
			v := map[string]int{"2013": V2013, "2019": V2019}[ver]
			p := newFakePlat(t, v)
			term := newTestTerm(t, p, ver)
			if term.Cfg.Token != "TOKEN123" {
				t.Fatalf("token=%q", term.Cfg.Token)
			}
			// 8103 设置：心跳 0x0001=15，车牌 0x0083
			s := p.send(0x8103, cat([]byte{2}, u32(1), []byte{4}, u32(15), u32(0x83), []byte{byte(len(gbk("粤B99999")))}, gbk("粤B99999")))
			if r := p.ackResult(s); r != 0 {
				t.Fatalf("8103 result=%d", r)
			}
			if term.Params.Int(1) != 15 || term.Cfg.Plate != "粤B99999" {
				t.Fatalf("参数未生效 %v %s", term.Params[1], term.Cfg.Plate)
			}
			// 8106 查询指定参数
			s = p.send(0x8106, cat([]byte{2}, u32(1), u32(0x83)))
			m := p.reply(0x0104, s, 5*time.Second)
			r := newReader(m.Body[2:])
			if n := r.u8(); n != 2 || r.u32() != 1 || r.u8() != 4 || r.u32() != 15 {
				t.Fatalf("0104 内容错误 %X", m.Body)
			}
			// 8104 全部参数：超过 1000 字节时分包，假平台已重组
			s = p.send(0x8104, nil)
			m = p.reply(0x0104, s, 5*time.Second)
			if int(m.Body[2]) < 80 {
				t.Fatalf("参数个数 %d", m.Body[2])
			}
			// 8107 属性
			p.send(0x8107, nil)
			m = p.expect(0x0107, 5*time.Second, nil)
			if want := map[int]int{V2013: 2 + 5 + 20 + 7 + 10, V2019: 2 + 5 + 30 + 30 + 10}[v]; len(m.Body) < want+2 {
				t.Fatalf("0107 长度 %d", len(m.Body))
			}
			// 8201 位置查询
			s = p.send(0x8201, nil)
			m = p.reply(0x0201, s, 5*time.Second)
			if len(m.Body) < 2+28 {
				t.Fatal("0201 过短")
			}
		})
	}
}

func TestTextQuestionControl(t *testing.T) {
	p := newFakePlat(t, V2019)
	term := newTestTerm(t, p, "2019")
	s := p.send(0x8300, cat([]byte{0x0C, 1}, gbk("请注意安全")))
	if p.ackResult(s) != 0 {
		t.Fatal("8300")
	}
	s = p.send(0x8302, cat([]byte{1}, lstr("今天天气?"), []byte{1}, u16(len(gbk("晴"))), gbk("晴"), []byte{2}, u16(len(gbk("雨"))), gbk("雨")))
	p.ackResult(s)
	if err := term.Do("answer", map[string]any{"serial": float64(s), "answerId": float64(2)}); err != nil {
		t.Fatal(err)
	}
	m := p.expect(0x0302, 5*time.Second, nil)
	if int(binary.BigEndian.Uint16(m.Body)) != s || m.Body[2] != 2 {
		t.Fatalf("0302 %X", m.Body)
	}
	// 8500 车门加锁（2019：标志 类型 参数）
	s = p.send(0x8500, []byte{1, 0, 1, 1})
	m = p.reply(0x0500, s, 5*time.Second)
	if binary.BigEndian.Uint32(m.Body[6:])&(1<<12) == 0 {
		t.Fatal("0500 状态未加锁")
	}
	// 8500 应答方式：先 0001 再 0500
	if err := term.Do("setConfig", map[string]any{"reply8500": "ack0500"}); err != nil {
		t.Fatal(err)
	}
	s = p.send(0x8500, []byte{1, 0, 1, 0})
	if r := p.ackResult(s); r != 0 {
		t.Fatalf("8500 通用应答结果 %d", r)
	}
	m = p.reply(0x0500, s, 5*time.Second)
	if binary.BigEndian.Uint32(m.Body[6:])&(1<<12) != 0 {
		t.Fatal("0500 状态未解锁")
	}
	if err := term.Do("setConfig", map[string]any{"reply8500": "bogus"}); err == nil {
		t.Fatal("非法 reply8500 应报错")
	}
	term.Do("setConfig", map[string]any{"reply8500": "0500"})
	// 8203 人工确认报警
	term.Do("setAlarm", map[string]any{"bit": float64(0), "on": float64(1)})
	p.expect(0x0200, 3*time.Second, func(m *Msg) bool { return binary.BigEndian.Uint32(m.Body)&1 != 0 })
	s = p.send(0x8203, cat(u16(0), u32(1)))
	p.ackResult(s)
	if term.alarmWord()&1 != 0 {
		t.Fatal("紧急报警未清除")
	}
	// 8202 临时跟踪 1s
	s = p.send(0x8202, cat(u16(1), u32(10)))
	p.ackResult(s)
	p.expect(0x0200, 3*time.Second, nil)
	p.expect(0x0200, 3*time.Second, nil)
}

func TestAreasRecorderMedia(t *testing.T) {
	p := newFakePlat(t, V2019)
	term := newTestTerm(t, p, "2019")
	// 8600 圆形区域：属性 bit3 进区域报警给平台，中心为当前位置外 500m，半径 300m
	lat, lng := moveBy(term.Lat, term.Lng, 90, 500)
	item := cat(u32(7), u16(1<<3), u32(int(lat*1e6)), u32(int(lng*1e6)), u32(300), u16(len(gbk("园区"))), gbk("园区"))
	s := p.send(0x8600, cat([]byte{0, 1}, item))
	if p.ackResult(s) != 0 || len(term.Areas) != 1 || term.Areas[0].Name != "园区" {
		t.Fatalf("8600 %+v", term.Areas)
	}
	p.send(0x8608, cat([]byte{1}, u32(0)))
	m := p.expect(0x0608, 5*time.Second, nil)
	if m.Body[0] != 1 || binary.BigEndian.Uint32(m.Body[1:]) != 1 || !bytes.Equal(m.Body[5:], item) {
		t.Fatalf("0608 %X", m.Body)
	}
	// 驶入区域：东行 120km/h，应产生 bit20 报警 + 0x12 附加信息
	term.mu.Lock()
	term.dir = 90
	term.mu.Unlock()
	term.Do("setMotion", map[string]any{"mode": "route", "speed": float64(0)})
	term.mu.Lock()
	term.motion.Mode, term.speed, term.motion.Speed = "drive", 120, 120
	term.Params[0x0055] = "200"
	term.mu.Unlock()
	go func() { // drive 模式方向会抖动，保持向东
		for i := 0; i < 40; i++ {
			term.mu.Lock()
			term.dir = 90
			term.mu.Unlock()
			time.Sleep(200 * time.Millisecond)
		}
	}()
	p.expect(0x0200, 8*time.Second, func(m *Msg) bool {
		return binary.BigEndian.Uint32(m.Body)&(1<<20) != 0 && bytes.Equal(findAttr(m.Body, 0x12), cat([]byte{1}, u32(7), []byte{0}))
	})
	term.Do("setMotion", map[string]any{"mode": "stop"})

	// 8700 行驶记录仪 08H 速度记录
	s = p.send(0x8700, cat([]byte{0x08}, gbFrame(0xAA, 0x75, 0x08, cat(bcdTime(time.Now().Add(-time.Hour)), bcdTime(time.Now()), u16(2)))))
	m = p.reply(0x0700, s, 5*time.Second)
	if m.Body[2] != 8 || m.Body[3] != 0x55 || m.Body[4] != 0x7A || int(binary.BigEndian.Uint16(m.Body[6:])) != 2*126 {
		t.Fatalf("0700 %X", m.Body[:12])
	}
	// 8801 拍照 2 张实时上传 -> 0805 + 0800 + 0801(JPEG)
	s = p.send(0x8801, []byte{1, 0, 2, 0, 1, 0, 1, 5, 128, 64, 64, 128})
	m = p.reply(0x0805, s, 5*time.Second)
	if m.Body[2] != 0 || binary.BigEndian.Uint16(m.Body[3:]) != 2 {
		t.Fatalf("0805 %X", m.Body)
	}
	m = p.expect(0x0801, 8*time.Second, nil)
	if !bytes.HasPrefix(m.Body[36:], []byte{0xFF, 0xD8}) {
		t.Fatal("0801 不是 JPEG")
	}
	s = p.send(0x8802, cat([]byte{0, 0, 0}, make([]byte, 12)))
	m = p.reply(0x0802, s, 5*time.Second)
	if binary.BigEndian.Uint16(m.Body[2:]) < 2 {
		t.Fatalf("0802 检索数 %d", binary.BigEndian.Uint16(m.Body[2:]))
	}
	// 不支持的通道
	s = p.send(0x8801, []byte{9, 0, 1, 0, 1, 0, 1, 5, 128, 64, 64, 128})
	if m = p.reply(0x0805, s, 5*time.Second); m.Body[2] != 2 {
		t.Fatal("通道 9 应返回 2")
	}
}

func TestUpgradeAndVehicleControl(t *testing.T) {
	for _, ver := range []string{"2013", "2019"} {
		t.Run(ver, func(t *testing.T) {
			v := map[string]int{"2013": V2013, "2019": V2019}[ver]
			p := newFakePlat(t, v)
			term := newTestTerm(t, p, ver)
			// 8108：2013 制造商ID BYTE[5]，2019 BYTE[11]
			makerLen := map[int]int{V2013: 5, V2019: 11}[v]
			data := bytes.Repeat([]byte{0xAB}, 300)
			s := p.send(0x8108, cat([]byte{0}, fixed("SIMMK", makerLen), lstr("FW9.9"), u32(len(data)), data))
			if p.ackResult(s) != 0 {
				t.Fatal("8108 应答")
			}
			m := p.expect(0x0108, 6*time.Second, nil)
			if !bytes.Equal(m.Body, []byte{0, 0}) {
				t.Fatalf("0108 %X", m.Body)
			}
			waitFor(t, time.Second, func() bool { term.mu.Lock(); defer term.mu.Unlock(); return term.Cfg.FwVer == "FW9.9" })
			locked := func(m *Msg) bool { return binary.BigEndian.Uint32(m.Body[2+4:])&(1<<12) != 0 }
			if v == V2013 {
				s = p.send(0x8500, []byte{1})
				if !locked(p.reply(0x0500, s, 5*time.Second)) {
					t.Fatal("2013 加锁失败")
				}
				s = p.send(0x8500, []byte{0})
				if locked(p.reply(0x0500, s, 5*time.Second)) {
					t.Fatal("2013 解锁失败")
				}
				return
			}
			// 2019：控制类型数量 + (0x0001 车门, 0 锁闭 / 1 开启)
			s = p.send(0x8500, cat(u16(1), u16(1), []byte{0}))
			if !locked(p.reply(0x0500, s, 5*time.Second)) {
				t.Fatal("2019 车门锁闭失败")
			}
			s = p.send(0x8500, cat(u16(1), u16(1), []byte{1}))
			if locked(p.reply(0x0500, s, 5*time.Second)) {
				t.Fatal("2019 车门开启失败")
			}
			s = p.send(0x8500, cat(u16(1), u16(0xF001), []byte{1}))
			if r := p.ackResult(s); r != 3 {
				t.Fatalf("不支持的控制类型应回 3，实际 %d", r)
			}
		})
	}
}

func TestNewParamsAndExtraInfo(t *testing.T) {
	p := newFakePlat(t, V2019)
	term := newTestTerm(t, p, "2019")
	now := time.Now()
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	period := []byte{byte(from.Hour()), byte(from.Minute()), byte(to.Hour()), byte(to.Minute())}
	av := hexBytes("0105001e19000003000003001e19000008000007ff01", 21)
	single := cat([]byte{1}, []byte{2}, av[:9], av[9:18], u16(0x3F))
	can := hexBytes("00000064d8fef100", 8)
	items := []struct {
		id int
		v  []byte
	}{{0x0023, gbk("CMIOT")}, {0x0024, gbk("user")}, {0x0025, gbk("pass")}, {0x0026, gbk("1.2.3.4:7611;5.6.7.8:7612")},
		{0x0032, period}, {0x0075, av}, {0x0077, single}, {0x0150, can}}
	body := []byte{byte(len(items))}
	for _, it := range items {
		body = cat(body, u32(it.id), []byte{byte(len(it.v))}, it.v)
	}
	if r := p.ackResult(p.send(0x8103, body)); r != 0 {
		t.Fatalf("8103 result=%d", r)
	}
	term.mu.Lock()
	got26, got32 := term.Params[0x0026], term.Params[0x0032]
	term.mu.Unlock()
	if got26 != "1.2.3.4:7611;5.6.7.8:7612" || len(got32) != 8 {
		t.Fatalf("参数类型错误 0026=%q 0032=%q", got26, got32)
	}
	q := []byte{byte(len(items))}
	for _, it := range items {
		q = cat(q, u32(it.id))
	}
	m := p.reply(0x0104, p.send(0x8106, q), 5*time.Second)
	r := newReader(m.Body[2:])
	if n := r.u8(); n != len(items) {
		t.Fatalf("0104 参数数 %d", n)
	}
	for _, it := range items {
		id, l := r.u32(), r.u8()
		if id != it.id || !bytes.Equal(r.take(l), it.v) {
			t.Fatalf("0104 参数 %04X 回读不一致", it.id)
		}
	}
	// 0x0077 结构解析
	if f := paramFields(0x0077, term.Params[0x0077]); len(f["items"].([]map[string]any)) != 1 {
		t.Fatalf("0077 解析 %v", f)
	}
	// 违规行驶时段内行驶 -> 报警位 bit15；扩展信号 / IO / 模拟量
	term.Do("setConfig", map[string]any{"autoAlarms": float64(1)})
	term.Do("setSignal", map[string]any{"bit": float64(3), "on": float64(1)})
	term.Do("setIO", map[string]any{"bit": float64(1), "on": float64(1)})
	term.Do("setAnalog", map[string]any{"ad0": float64(100), "ad1": float64(200)})
	term.Do("setMotion", map[string]any{"mode": "drive", "speed": float64(40)})
	m = p.expect(0x0200, 8*time.Second, func(m *Msg) bool { return binary.BigEndian.Uint32(m.Body)&(1<<15) != 0 })
	if a := findAttr(m.Body, 0x25); binary.BigEndian.Uint32(a) != 1<<3 {
		t.Fatalf("0x25 %X", a)
	}
	if a := findAttr(m.Body, 0x2A); binary.BigEndian.Uint16(a) != 1<<1 {
		t.Fatalf("0x2A %X", a)
	}
	if a := findAttr(m.Body, 0x2B); binary.BigEndian.Uint32(a) != 200<<16|100 {
		t.Fatalf("0x2B %X", a)
	}
	term.Do("setMotion", map[string]any{"mode": "stop"})
	// 页面逐字段设置参数
	if err := term.Do("setParam", map[string]any{"id": "0032", "fields": map[string]any{"sh": 22.0, "sm": 50.0, "eh": 10.0, "em": 30.0}}); err != nil {
		t.Fatal(err)
	}
	if term.Params[0x0032] != "16320a1e" {
		t.Fatalf("0032=%s", term.Params[0x0032])
	}
	if err := term.Do("setParam", map[string]any{"id": "0001", "value": "0"}); err == nil {
		t.Fatal("心跳 0 应校验失败")
	}
}

func TestProfileExportImport(t *testing.T) {
	stateDir = t.TempDir()
	a := NewTerminal(Config{Phone: "13900000001", Version: "2013"})
	a.Params[0x0029] = "15"
	a.Params[0x0150] = "00000064d8fef100"
	a.Driver.Name = "李四"
	a.ExtSignal = 5
	prof := a.exportProfile()
	b, _ := json.Marshal(prof)
	var back Profile
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	c := NewTerminal(Config{Phone: "13900000002"})
	if err := c.importProfile(back); err != nil {
		t.Fatal(err)
	}
	if c.Cfg.Phone != "13900000001" || c.Cfg.Version != "2013" || c.Params[0x0029] != "15" || c.Params[0x0150] != "00000064d8fef100" ||
		c.Driver.Name != "李四" || c.ExtSignal != 5 || c.Params[0x0001] != "30" {
		t.Fatalf("导入不一致 %+v", c.Cfg)
	}
	back.Params[0x0001] = "abc"
	if err := c.importProfile(back); err == nil {
		t.Fatal("非法参数值应导入失败")
	}
}
