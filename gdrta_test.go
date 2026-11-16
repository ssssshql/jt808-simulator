package main

// 粤标 T/GDRTA 002—2020 编解码与终端行为测试：字节长度、字段偏移均按标准表格逐项核对

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func newGDTerm(t *testing.T) *Terminal {
	t.Helper()
	stateDir = t.TempDir()
	Quiet = true
	term := NewTerminal(Config{Phone: "13800000009", Version: "2019"})
	term.mu.Lock()
	term.Cfg.SafetyStd = StdGD
	term.Cfg.DeviceID = "gd0001"
	term.fillDefaults()
	term.speed, term.alt = 66, 35
	term.mu.Unlock()
	return term
}

// fieldOffset 结构字段在参数中的起始字节
func fieldOffset(t *testing.T, s *Schema, key string) int {
	t.Helper()
	off := 0
	for _, f := range s.Fields {
		if f.Key == key {
			return off
		}
		off += f.Size
	}
	t.Fatalf("字段 %s 不存在", key)
	return -1
}

func TestGDAlarmFlagAndAttrs(t *testing.T) {
	term := newGDTerm(t)
	term.mu.Lock()
	defer term.mu.Unlock()
	term.ExtSignal = 1<<3 | 1<<4 // 左转 + 制动
	term.Driver.OnDuty = true

	// 表 5-16 报警标识号 40 字节
	ts := bcd("201231235958", 6)
	f := gdAlarmFlag("ab12", ts, 7, 3)
	if len(f) != 40 || string(f[:4]) != "AB12" || f[4] != 0 || !bytes.Equal(f[30:36], ts) || f[36] != 7 || f[37] != 3 || f[38] != 0 || f[39] != 0 {
		t.Fatalf("报警标识号 %X", f)
	}

	// 表 5-15 ADAS：长度 31 + 40；偏移 5 类型 12 车速 13 高程 29 车辆状态 31 报警标识号
	b, mark := term.gdAttr(0x64, 0x12, 2, 3, -1)
	v := b[2:]
	if b[0] != 0x64 || int(b[1]) != len(v) || len(v) != 71 {
		t.Fatalf("0x64 长度 %d", len(v))
	}
	if binary.BigEndian.Uint32(v) != 0 || v[4] != 1 || v[5] != 0x12 || v[6] != 2 || v[12] != 66 || binary.BigEndian.Uint16(v[13:]) != 35 {
		t.Fatalf("0x64 字段 %X", v[:15])
	}
	if vs := binary.BigEndian.Uint16(v[29:]); vs != 1|1<<1|1<<4|1<<5|1<<10 { // ACC 左转 制动 插卡 定位
		t.Fatalf("车辆状态 %016b", vs)
	}
	if !bytes.Equal(v[31:], mark) || string(mark[:6]) != "GD0001" || mark[36] != 0 || mark[37] != 3 {
		t.Fatalf("报警标识 %X", mark)
	}
	// 车道偏离：前车车速/距离/偏离类型有效；道路标识超限：标志类型/数据
	b, _ = term.gdAttr(0x64, 2, 1, 0, -1)
	if v := b[2:]; binary.BigEndian.Uint32(v) != 1 || v[7] == 0 || v[8] != 15 || v[9] != 1 || v[10] != 0 {
		t.Fatalf("车道偏离 %X", v[:12])
	}
	b, _ = term.gdAttr(0x64, 0x10, 1, 0, -1)
	if v := b[2:]; v[4] != 0 || v[10] != 1 || v[11] != 60 || v[7] != 0 { // 事件：标志状态不可用
		t.Fatalf("道路标志识别事件 %X", v[:12])
	}

	// 表 5-17 DSM：偏移 7 疲劳程度 8 预留 BYTE[4] 12 车速
	b, mark = term.gdAttr(0x65, 0x0D, 1, 2, 2)
	v = b[2:]
	if len(v) != 71 || v[4] != 2 || v[5] != 0x0D || v[7] != 0 || !bytes.Equal(v[8:12], make([]byte, 4)) || v[12] != 66 || !bytes.Equal(v[31:], mark) {
		t.Fatalf("0x65 %X", v[:31])
	}
	b2, m2 := term.gdAttr(0x65, 1, 1, 0, -1)
	if b2[2+7] != 5 { // 疲劳程度仅疲劳驾驶有效
		t.Fatalf("疲劳程度 %X", b2[2+7])
	}
	if bytes.Equal(m2[30:36], mark[30:36]) && m2[36] != mark[36]+1 || !bytes.Equal(m2[30:36], mark[30:36]) && m2[36] != 0 {
		t.Fatalf("同一时间点序号应累加 %d -> %d", mark[36], m2[36])
	}

	// 表 5-18/5-19 TPMS：24 报警标识号 64 列表总数 65 列表（位置 BYTE[2] 类型 WORD 胎压 胎温 电量）
	b, mark = term.gdAttr(0x66, 3, 1, 0, -1)
	v = b[2:]
	if len(v) != 65+10 || v[5] != 66 || !bytes.Equal(v[24:64], mark) || v[64] != 1 || v[65] != 0 || v[66] != 0 {
		t.Fatalf("0x66 %X", v)
	}
	if typ := binary.BigEndian.Uint16(v[67:]); typ != 1<<2 { // bit2 胎压过低
		t.Fatalf("TPMS 类型 %b", typ)
	}
	if binary.BigEndian.Uint16(v[69:]) != 560 || binary.BigEndian.Uint16(v[73:]) != 80 {
		t.Fatalf("TPMS 胎压/电量 %X", v[65:])
	}

	// 表 5-20 BSD：5 类型 6 车速 25 报警标识号
	b, mark = term.gdAttr(0x67, 2, 1, 0, -1)
	v = b[2:]
	if len(v) != 65 || v[5] != 2 || v[6] != 66 || !bytes.Equal(v[25:], mark) {
		t.Fatalf("0x67 %X", v)
	}
	if term.GD.AlarmID != 7 {
		t.Fatalf("报警 ID 应从 0 累加，当前 %d", term.GD.AlarmID)
	}

	// 0xF1/0xF2 安装/算法异常
	term.GD.InstallErr, term.GD.AlgoErr = 0x11, 0
	body := term.locBody(nil)
	if a := findAttr(body, 0xF1); len(a) != 4 || binary.BigEndian.Uint32(a) != 0x11 || findAttr(body, 0xF2) != nil {
		t.Fatalf("0xF1/F2 %X", a)
	}
	term.Cfg.SafetyStd = ""
	if findAttr(term.locBody(nil), 0xF1) != nil {
		t.Fatal("苏标不应附带 0xF1")
	}
}

func TestGDParamSchemas(t *testing.T) {
	want := map[int]int{0xF364: 64, 0xF365: 66, 0xF366: 36, 0xF367: 2}
	for id, n := range want {
		d := gdParamIndex[id]
		if d.Schema.size() != n || len(encodeValue(id, d.Def))/1 != n {
			t.Fatalf("%04X 长度 %d/%d，应为 %d", id, d.Schema.size(), len(d.Def)/2, n)
		}
	}
	// 表 5-10 关键偏移
	for k, off := range map[string]int{"speed": 0, "shotInt": 3, "shotDist": 5, "picRes": 9, "vidRes": 10, "alarmEn": 11, "eventEn": 15,
		"rsv1": 19, "obstDist": 20, "obstSpeed": 21, "lcTime": 25, "lcSpeed": 27, "ldwSpeed": 31, "fcwTime": 35, "fcwSpeed": 36,
		"pcwTime": 40, "pcwSpeed": 41, "hmwDist": 45, "hmwSpeed": 46, "tsrPhotos": 50, "tsrPhotoInt": 51, "sldSpeed": 52,
		"sldPhotoInt": 55, "aisleSpeed": 56, "aisleVideo": 57, "aislePhotoInt": 59, "rsv2": 60} {
		if got := fieldOffset(t, gdAdasSchema, k); got != off {
			t.Fatalf("F364 %s 偏移 %d，应为 %d", k, got, off)
		}
	}
	// 表 5-11 关键偏移
	for k, off := range map[string]int{"shotInt": 3, "alarmEn": 11, "eventEn": 15, "smokeInt": 19, "phoneInt": 21, "rsv1": 23,
		"fatigueSpeed": 26, "phoneSpeed": 30, "smokeSpeed": 34, "distractSpeed": 38, "abnormalSpeed": 42, "idTrigger": 46,
		"coverSpeed": 47, "beltSpeed": 48, "irSpeed": 52, "handsSpeed": 56, "playSpeed": 60, "playPhotoInt": 63, "rsv2": 64} {
		if got := fieldOffset(t, gdDsmSchema, k); got != off {
			t.Fatalf("F365 %s 偏移 %d，应为 %d", k, got, off)
		}
	}
	// 表 5-12：12 胎压单位 WORD 28 定时上报 30 保留 BYTE[6]
	for k, off := range map[string]int{"unit": 12, "normal": 14, "report": 28, "rsv": 30} {
		if got := fieldOffset(t, gdTpmsSchema, k); got != off {
			t.Fatalf("F366 %s 偏移 %d，应为 %d", k, got, off)
		}
	}
	// 默认值（标准给出的）
	adas, _ := hex.DecodeString(gdParamIndex[0xF364].Def)
	if adas[0] != 30 || adas[1] != 6 || binary.BigEndian.Uint16(adas[3:]) != 60 || adas[9] != 5 || adas[10] != 3 ||
		binary.BigEndian.Uint32(adas[11:]) != 0x000F0FFF || binary.BigEndian.Uint32(adas[15:]) != 3 || adas[35] != 27 || adas[50] != 1 {
		t.Fatalf("F364 默认值 %X", adas)
	}
	dsm, _ := hex.DecodeString(gdParamIndex[0xF365].Def)
	if binary.BigEndian.Uint16(dsm[3:]) != 3600 || binary.BigEndian.Uint32(dsm[11:]) != 0x000FFFFF || binary.BigEndian.Uint16(dsm[19:]) != 300 || dsm[46] != 1 {
		t.Fatalf("F365 默认值 %X", dsm)
	}
	// 字段级编辑往返
	f := paramFieldsStd(true, 0xF364, gdParamIndex[0xF364].Def)
	f["sldSpeed"], f["aisleVideo"] = 70.0, 9.0
	v, err := encodeParamFieldsStd(true, 0xF364, f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := hex.DecodeString(v)
	if len(b) != 64 || b[52] != 70 || b[57] != 9 {
		t.Fatalf("F364 编码 %X", b)
	}
	if d := pdefStd(true, 0xF370); d == nil || !d.ReadOnly || pdefStd(false, 0xF370) != nil {
		t.Fatal("F370 仅粤标、只读")
	}
}

func TestGDParamMergeAndNormalize(t *testing.T) {
	def := gdParamIndex[0xF364].Def
	nv := bytes.Repeat([]byte{0xFF}, 64)
	nv[1] = 3                                       // 音量 3
	copy(nv[3:5], []byte{0x00, 0x0A})               // 定时拍照 10 秒
	copy(nv[11:15], []byte{0xFF, 0xFF, 0xFF, 0xFE}) // 报警使能（不全是 FF → 修改）
	b, _ := hex.DecodeString(gdMerge(0xF364, def, nv))
	d, _ := hex.DecodeString(def)
	if len(b) != 64 || b[0] != d[0] || b[1] != 3 || binary.BigEndian.Uint16(b[3:]) != 10 || binary.BigEndian.Uint16(b[5:]) != 200 ||
		binary.BigEndian.Uint32(b[11:]) != 0xFFFFFFFE || b[60] != 0xFF { // 保留字段照常覆盖
		t.Fatalf("合并 %X", b)
	}
	// 苏标 56 字节默认值迁移为粤标 64 字节；较短值用粤标默认值补齐
	if gdNormalize(0xF364, paramIndex[0xF364].Def) != def {
		t.Fatal("苏标默认值应迁移为粤标默认值")
	}
	short := strings.Repeat("01", 56)
	if n := gdNormalize(0xF364, short); len(n) != 128 || n[:112] != short {
		t.Fatalf("补齐 %s", n)
	}
}

func TestGDRecordFileAndNames(t *testing.T) {
	term := newGDTerm(t)
	term.mu.Lock()
	rec := term.gdRecordFile(20)
	term.mu.Unlock()
	if len(rec) != 20*64 {
		t.Fatalf("记录文件长度 %d", len(rec))
	}
	for i := 0; i < 20; i++ {
		blk := rec[i*64 : (i+1)*64]
		var sum byte
		for _, x := range blk[:63] {
			sum += x
		}
		if blk[63] != sum || binary.BigEndian.Uint32(blk) != 20 || binary.BigEndian.Uint32(blk[4:]) != uint32(i+1) {
			t.Fatalf("数据块 %d 校验/序号错误 %X", i, blk)
		}
		if binary.BigEndian.Uint16(blk[40:]) != 100 { // Z 轴 1g
			t.Fatalf("Z 轴加速度 %X", blk[40:42])
		}
	}
	files := gdAttachFiles(adasAlarm{0x64, 0x12, 1}, 4, "ALM1", rec)
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "00_64_6412_0_ALM1.jpg,00_64_6412_1_ALM1.jpg,02_64_6412_0_ALM1.mp4,03_0_6412_0_ALM1.bin" || files[3].Type != 3 {
		t.Fatalf("附件命名 %v", names)
	}
	if f := gdAttachFiles(adasAlarm{0x65, 0x11, 1}, 1, "N", nil); f[0].Name != "04_65_6511_0_N.jpg" || f[0].Type != 4 {
		t.Fatalf("面部特征图片 %v", f[0].Name)
	}
	if f := gdAttachFiles(adasAlarm{0x66, 2, 1}, 1, "N", nil); f[0].Name != "00_0_6602_0_N.jpg" {
		t.Fatalf("TPMS 通道应为 0：%v", f[0].Name)
	}
	// 1210：终端ID 30 + 报警标识 40 + 报警编号 32 + 信息类型 + 附件数 + 列表
	flag := gdAlarmFlag("GD0001", bcdNow(), 0, 4)
	b := b1210(V2019, "GD0001", flag, fixed("ALM1", 32), files, 1)
	if b[102] != 1 || b[103] != 4 || int(b[104]) != len(files[0].Name) || !bytes.Equal(b[30:70], flag) {
		t.Fatalf("1210 %X", b[:110])
	}
	// 9208：IP 长度 k、IP、TCP、UDP、报警标识 40、报警编号 32、预留 16
	body := cat(lstr("10.0.0.1"), u16(7000), u16(7001), flag, fixed("ALM1", 32), make([]byte, 16))
	info, err := parse9208(V2019, body)
	if err != nil || info.IP != "10.0.0.1" || info.TCP != 7000 || info.UDP != 7001 || !bytes.Equal(info.Flag, flag) || info.FileTotal != 4 {
		t.Fatalf("9208 %+v %v", info, err)
	}
}

func TestGDUpgradeName(t *testing.T) {
	f := parseGDUpgradeName("http://h/fw/03_SIMMK_ADAS1_SV1.0.0_SV2.1.0.bin?x=1")
	if !f.OK || f.Type != 0x64 || f.Maker != "SIMMK" || f.Depend != "SV1.0.0" || f.SwVer != "SV2.1.0" {
		t.Fatalf("%+v", f)
	}
	for name, typ := range map[string]int{"01_A_B_C_D.pkg": 0, "04_A_B_C_D.x": 0x65, "05_A_B_C_D.x": 0x67, "06_A_B_C_D.x": 0x66} {
		if f := parseGDUpgradeName("ftp://x/" + name); !f.OK || f.Type != typ {
			t.Fatalf("%s %+v", name, f)
		}
	}
	if parseGDUpgradeName("ftp://x/firmware.bin").OK {
		t.Fatal("不符合命名规则")
	}
}

// gdAttachServer 附件服务器：记录 1210 消息体；dropOnce 时第一次会话在第二个文件的 1211 后断开
func gdAttachServer(t *testing.T, dropOnce bool) (int, chan []byte, chan string) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	b1210s, done := make(chan []byte, 8), make(chan string, 16)
	var mu sync.Mutex
	drop := dropOnce
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				serial, n1211 := 0, 0
				reply := func(id int, body []byte) {
					serial++
					c.Write(Frame(id, "13800000009", serial, body, V2019, 0, 0))
				}
				var buf []byte
				tmp := make([]byte, 65536)
				for {
					n, err := c.Read(tmp)
					if err != nil {
						return
					}
					buf = append(buf, tmp[:n]...)
					for len(buf) > 0 {
						if bytes.HasPrefix(buf, []byte{0x30, 0x31, 0x63, 0x64}) {
							if len(buf) < 62 {
								break
							}
							l := int(binary.BigEndian.Uint32(buf[58:]))
							if len(buf) < 62+l {
								break
							}
							buf = buf[62+l:]
							continue
						}
						e := bytes.IndexByte(buf[1:], 0x7e)
						if e < 0 {
							break
						}
						m := Parse(buf[1 : e+1])
						buf = buf[e+2:]
						switch m.ID {
						case 0x1210:
							b1210s <- m.Body
							reply(0x8001, cat(u16(m.Serial), u16(m.ID), []byte{0}))
						case 0x1211:
							n1211++
							mu.Lock()
							d := drop && n1211 == 2
							if d {
								drop = false
							}
							mu.Unlock()
							if d {
								return
							}
							reply(0x8001, cat(u16(m.Serial), u16(m.ID), []byte{0}))
						case 0x1212:
							r := newReader(m.Body)
							name := r.lstr()
							typ := r.u8()
							done <- name
							reply(0x9212, cat(lstr(name), []byte{byte(typ), 0, 0}))
						}
					}
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, b1210s, done
}

func TestGDTerminal(t *testing.T) {
	gdStep = 10 * time.Millisecond
	p := newFakePlat(t, V2019)
	stateDir = t.TempDir()
	Quiet = true
	// 选择 2013 + 粤标：强制按 2019 帧格式
	term := NewTerminal(Config{Host: "127.0.0.1", Port: p.port(), Phone: "13800000009", Version: "2013"})
	if err := term.Do("setConfig", map[string]any{"safetyStd": "gd", "deviceId": "GD0001"}); err != nil {
		t.Fatal(err)
	}
	if term.Cfg.ver() != V2019 || term.Params[0xF370] != "1" || len(term.Params[0xF364]) != 128 || len(term.Params[0xF365]) != 132 {
		t.Fatalf("粤标初始化 ver=%d F370=%q F364=%d", term.Cfg.ver(), term.Params[0xF370], len(term.Params[0xF364]))
	}
	if !term.State()["safety"].(map[string]any)["verForced"].(bool) {
		t.Fatal("页面应提示 2013 被强制为 2019")
	}
	go term.motionLoop()
	term.Start()
	t.Cleanup(func() { term.Stop("测试结束") })
	p.expect(0x0102, 5*time.Second, nil)
	waitFor(t, 3*time.Second, func() bool { return term.State()["conn"].(ConnView).Status == "online" })

	// 报警：实线变道 0x12（苏标没有此类型）；页面 POST 中 type 是动作名，报警类型用 alarmType
	if err := term.Do("adas", map[string]any{"type": "adas", "kind": "adas", "alarmType": float64(0x12), "level": float64(2), "files": float64(3)}); err != nil {
		t.Fatal(err)
	}
	m := p.expect(0x0200, 3*time.Second, func(m *Msg) bool { return findAttr(m.Body, 0x64) != nil })
	if m.Version != V2019 {
		t.Fatalf("应为 2019 帧：%d", m.Version)
	}
	a := findAttr(m.Body, 0x64)
	if len(a) != 71 || a[5] != 0x12 {
		t.Fatalf("0x64 %X", a)
	}
	flag := a[31:]

	// 9208 → 附件服务器；第一次会话中途断开，重连后 1210 信息类型 0x01 补传剩余文件
	port, b1210s, done := gdAttachServer(t, true)
	s := p.send(0x9208, cat(lstr("127.0.0.1"), u16(port), u16(0), flag, fixed("ALM0001", 32), make([]byte, 16)))
	if p.ackResult(s) != 0 {
		t.Fatal("9208")
	}
	first := <-b1210s
	if first[102] != 0 || first[103] != 3 {
		t.Fatalf("1210 首次 %X", first[100:106])
	}
	var second []byte
	select {
	case second = <-b1210s:
	case <-time.After(10 * time.Second):
		t.Fatal("未重连补传")
	}
	if second[102] != 1 || second[103] != 2 {
		t.Fatalf("1210 补传 信息类型=%d 附件数=%d", second[102], second[103])
	}
	var names []string
	for len(names) < 3 {
		select {
		case n := <-done:
			names = append(names, n)
		case <-time.After(10 * time.Second):
			t.Fatalf("附件未完成 %v", names)
		}
	}
	if names[2] != "03_0_6412_0_ALM0001.bin" {
		t.Fatalf("附件 %v", names)
	}

	// 8103：F364 0xFF 字段不修改；F370 只读
	nv := bytes.Repeat([]byte{0xFF}, 64)
	nv[1] = 2
	s = p.send(0x8103, cat([]byte{2}, u32(0xF364), []byte{64}, nv, u32(0xF370), []byte{1, 9}))
	if p.ackResult(s) != 0 {
		t.Fatal("8103")
	}
	term.mu.Lock()
	v364, v370 := term.Params[0xF364], term.Params[0xF370]
	term.mu.Unlock()
	if b, _ := hex.DecodeString(v364); b[0] != 30 || b[1] != 2 || v370 != "1" {
		t.Fatalf("8103 合并 F364=%s F370=%s", v364, v370)
	}
	// 8106 查询 F370 / F365
	s = p.send(0x8106, cat([]byte{2}, u32(0xF370), u32(0xF365)))
	r := p.reply(0x0104, s, 5*time.Second)
	if r.Body[2] != 2 || binary.BigEndian.Uint32(r.Body[3:]) != 0xF365 && binary.BigEndian.Uint32(r.Body[3:]) != 0xF370 {
		t.Fatalf("0104 %X", r.Body)
	}
	if !bytes.Contains(r.Body, cat(u32(0xF370), []byte{1, 1})) || !bytes.Contains(r.Body, cat(u32(0xF365), []byte{66})) {
		t.Fatalf("0104 F370/F365 %X", r.Body)
	}

	// 8900 F7 查询 ADAS + DSM 状态 → 0001 + 0900
	term.Do("gdPeriph", map[string]any{"id": float64(0x65), "work": float64(4), "alarm": float64(1 << 3)})
	s = p.send(0x8900, []byte{0xF7, 2, 0x64, 0x65})
	if p.ackResult(s) != 0 {
		t.Fatal("8900")
	}
	m = p.expect(0x0900, 3*time.Second, nil)
	want := cat([]byte{0xF7, 2, 0x64, 5, 1}, u32(0), []byte{0x65, 5, 4}, u32(8))
	if !bytes.Equal(m.Body, want) {
		t.Fatalf("0900 F7 %X，应为 %X", m.Body, want)
	}
	s = p.send(0x8900, []byte{0xF8, 1, 0x66})
	p.ackResult(s)
	m = p.expect(0x0900, 3*time.Second, nil)
	rd := newReader(m.Body[4:])
	if m.Body[0] != 0xF8 || m.Body[1] != 1 || m.Body[2] != 0x66 || int(m.Body[3]) != len(m.Body)-4 || rd.lstr() != "SIMMK" || rd.lstr() != "SIM-TPMS" {
		t.Fatalf("0900 F8 %X", m.Body)
	}

	// 8105 无线升级 ADAS：1FC4 进度（流水号=8105 流水号）→ 0108 [0x64, 0]
	s = p.send(0x8105, append([]byte{1}, gbk("ftp://up.example/03_SIMMK_ADAS_SV1.0.0_SV2.0.0.bin;;;;;;;SIMMK;HW1.0;;0")...))
	if p.ackResult(s) != 0 {
		t.Fatal("8105")
	}
	var states []int
	for {
		m = p.expect(0x1FC4, 5*time.Second, nil)
		if len(m.Body) != 6 || int(binary.BigEndian.Uint16(m.Body)) != s || m.Body[2] != 0x64 {
			t.Fatalf("1FC4 %X", m.Body)
		}
		states = append(states, int(m.Body[3]))
		if m.Body[3] == 5 {
			break
		}
	}
	if states[0] != 1 || !bytes.Contains(intsToBytes(states), []byte{2, 4}) {
		t.Fatalf("升级状态序列 %v", states)
	}
	if m = p.expect(0x0108, 3*time.Second, nil); !bytes.Equal(m.Body, []byte{0x64, 0}) {
		t.Fatalf("0108 %X", m.Body)
	}
	term.mu.Lock()
	sw := term.periph(0x64).SwVer
	term.mu.Unlock()
	if sw != "SV2.0.0" {
		t.Fatalf("ADAS 版本 %s", sw)
	}
	// 再次升级到相同版本 → 0x09 / 0x12
	s = p.send(0x8105, append([]byte{1}, gbk("ftp://up.example/03_SIMMK_ADAS_SV2.0.0_SV2.0.0.bin")...))
	p.ackResult(s)
	if m = p.expect(0x1FC4, 3*time.Second, nil); m.Body[3] != 9 {
		t.Fatalf("版本相同 %X", m.Body)
	}
	if m = p.expect(0x0108, 3*time.Second, nil); !bytes.Equal(m.Body, []byte{0x64, 0x12}) {
		t.Fatalf("0108 %X", m.Body)
	}
	// 模拟安装失败
	term.Do("gdSet", map[string]any{"upgradeSim": "installfail"})
	s = p.send(0x8105, append([]byte{1}, gbk("ftp://up.example/01_SIMMK_T1_FW1.0.0_FW3.0.0.bin")...))
	p.ackResult(s)
	if m = p.expect(0x0108, 5*time.Second, nil); !bytes.Equal(m.Body, []byte{0, 1}) {
		t.Fatalf("安装失败 0108 %X", m.Body)
	}

	// 8801 通道 0x65：DSM 外设拍照
	s = p.send(0x8801, []byte{0x65, 0, 1, 0, 0, 0, 5, 3, 128, 64, 64, 128})
	r = p.reply(0x0805, s, 5*time.Second)
	if r.Body[2] != 0 || binary.BigEndian.Uint16(r.Body[3:]) != 3 {
		t.Fatalf("0805 %X", r.Body)
	}
	if m = p.expect(0x0800, 5*time.Second, nil); m.Body[7] != 0x65 {
		t.Fatalf("0800 通道 %X", m.Body)
	}

	// 苏标不支持的类型被拒绝；切回苏标后 F370 移除、F364 恢复苏标默认
	term.Stop("切换标准")
	if err := term.Do("setConfig", map[string]any{"safetyStd": "jsatl"}); err != nil {
		t.Fatal(err)
	}
	term.mu.Lock()
	_, has := term.Params[0xF370]
	term.mu.Unlock()
	if has || term.Cfg.ver() != V2013 {
		t.Fatal("切回苏标后应移除 F370 且恢复 2013 帧格式")
	}
	if err := term.raiseADAS("adas", 0x12, 1, 0, -1); err == nil || !strings.Contains(err.Error(), "苏标") {
		t.Fatalf("苏标应拒绝 0x12：%v", err)
	}
}

func intsToBytes(a []int) []byte {
	var b []byte
	for _, x := range a {
		b = append(b, byte(x))
	}
	return b
}
