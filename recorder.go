package main

// 行驶记录仪（GB/T 19056-2012）：8700 采集 -> 0700，8701 参数下传 -> 0001

import (
	"fmt"
	"math"
	"time"
)

type RecorderState struct {
	Install string `json:"install"` // 安装日期 YYMMDDhhmmss
	Pulse   int    `json:"pulse"`   // 脉冲系数
	Initial int    `json:"initial"` // 初始里程 0.1km
	Class   string `json:"class"`   // 车牌分类
}

var recNames = map[int]string{0x00: "执行标准版本", 0x01: "驾驶人信息", 0x02: "实时时间", 0x03: "累计行驶里程", 0x04: "脉冲系数",
	0x05: "车辆信息", 0x06: "状态信号配置", 0x07: "唯一性编号", 0x08: "行驶速度记录", 0x09: "位置信息记录",
	0x10: "事故疑点记录", 0x11: "超时驾驶记录", 0x12: "驾驶人身份记录", 0x13: "外部供电记录", 0x14: "参数修改记录",
	0x15: "速度状态日志", 0x82: "设置车辆信息", 0x83: "设置初次安装日期", 0x84: "设置状态量配置", 0xC2: "设置记录仪时间",
	0xC3: "设置脉冲系数", 0xC4: "设置初始里程"}

func init() {
	handlers[0x8700] = (*Terminal).h8700
	handlers[0x8701] = (*Terminal).h8701
}

func gbFrame(head0, head1 byte, cmd int, data []byte) []byte {
	raw := cat([]byte{head0, head1, byte(cmd)}, u16(len(data)), []byte{0}, data)
	return append(raw, bcc(raw))
}

// gbData 取 GB 帧（AA 75 命令 长度 保留 数据 校验）中的数据；不是帧格式时原样返回
func gbData(b []byte) []byte {
	if len(b) >= 7 && b[0] == 0xAA && b[1] == 0x75 {
		n := int(b[3])<<8 | int(b[4])
		if 6+n <= len(b) {
			return b[6 : 6+n]
		}
	}
	return b
}

func bcdN(v, n int) []byte { return bcd(fmt.Sprintf("%0*d", n*2, v), n) }

func (t *Terminal) recPos() []byte { // 经度(4) 纬度(4) 高度(2)，单位 0.0001 分
	return cat(u32(int(t.Lng*600000)), u32(int(t.Lat*600000)), u16(t.alt))
}

// recBlocks 生成 08H~15H 记录块：从 to 往前按块时长倒推，最多 n 块
func (t *Terminal) recBlocks(cmd int, from, to time.Time, n int) []byte {
	if to.IsZero() || to.After(time.Now()) {
		to = time.Now()
	}
	n = max(min(n, 5), 1)
	step := map[int]time.Duration{0x08: time.Minute, 0x09: time.Hour, 0x10: 10 * time.Minute, 0x11: 4 * time.Hour,
		0x12: time.Hour, 0x13: 3 * time.Hour, 0x14: 6 * time.Hour, 0x15: time.Hour}[cmd]
	lic := fixed(t.Driver.LicenseNo, 18)
	var out []byte
	for i := 0; i < n; i++ {
		at := to.Add(-time.Duration(i) * step).Truncate(time.Minute)
		if !from.IsZero() && at.Before(from) {
			break
		}
		ts := bcdTime(at)
		switch cmd {
		case 0x08: // 60 秒 x (速度, 状态)
			out = append(out, ts...)
			for s := 0; s < 60; s++ {
				out = append(out, byte(40+20*math.Sin(float64(s)/10)), 0x01)
			}
		case 0x09: // 60 分钟 x (位置10 + 速度1)
			out = append(out, ts...)
			for s := 0; s < 60; s++ {
				out = cat(out, t.recPos(), []byte{50})
			}
		case 0x10: // 停车时间 + 驾驶证 + 100 x 0.2s (速度,状态) + 位置
			out = cat(out, ts, lic)
			for s := 0; s < 100; s++ {
				out = append(out, byte(max(0, 60-s)), 0x01)
			}
			out = cat(out, t.recPos())
		case 0x11:
			out = cat(out, lic, bcdTime(at.Add(-4*time.Hour-10*time.Minute)), ts, t.recPos(), t.recPos())
		case 0x12: // 1 登录 2 退出
			out = cat(out, ts, lic, []byte{byte(1 + i%2)})
		case 0x13: // 1 通电 2 断电
			out = cat(out, ts, []byte{byte(1 + i%2)})
		case 0x14:
			out = cat(out, ts, []byte{0x82})
		case 0x15: // 状态 1 正常 + 开始/结束 + 60 x (记录速度, 参考速度)
			out = cat(out, []byte{1}, bcdTime(at.Add(-time.Minute)), ts)
			for s := 0; s < 60; s++ {
				out = append(out, 50, 51)
			}
		}
	}
	return out
}

// recData 采集命令对应的数据块。调用方持有 t.mu
func (t *Terminal) recData(cmd int, req []byte) []byte {
	rs := &t.Rec
	plate := t.Cfg.Plate
	switch cmd {
	case 0x00:
		return []byte{0x12, 0x00}
	case 0x01:
		return fixed(t.Driver.LicenseNo, 18)
	case 0x02:
		return bcdNow()
	case 0x03:
		return cat(bcdNow(), bcd(rs.Install, 6), bcdN(rs.Initial, 4), bcdN(int(t.Mileage*10)%100000000, 4))
	case 0x04:
		return cat(bcdNow(), u16(rs.Pulse))
	case 0x05:
		return cat(fixedPad(t.Cfg.VIN, 17, ' '), fixed(plate, 12), fixed(rs.Class, 12))
	case 0x06:
		names := []string{"近光", "远光", "右转向", "左转向", "制动", "自定义1", "自定义2", "自定义3"}
		b := cat(bcdNow(), []byte{0x1F})
		for _, n := range names {
			b = cat(b, fixed(n, 10))
		}
		return b
	case 0x07:
		return cat(fixed("C000001", 7), fixed(t.Cfg.Model, 16), bcd("240101", 3), u32(1), make([]byte, 5))
	}
	if cmd >= 0x08 && cmd <= 0x15 {
		r := newReader(req)
		from, to, n := r.time6(), r.time6(), r.u16()
		if r.err != nil {
			n = 1
		}
		return t.recBlocks(cmd, from, to, n)
	}
	return nil
}

func (t *Terminal) h8700(m *Msg) {
	r := newReader(m.Body)
	cmd := r.u8()
	req := gbData(r.rest())
	t.mu.Lock()
	if t.Rec.Install == "" {
		t.Rec = RecorderState{Install: "240101080000", Pulse: 3600, Initial: 0, Class: "大型货车"}
	}
	data := t.recData(cmd, req)
	t.mu.Unlock()
	if data == nil {
		t.ack(m, 3)
		return
	}
	t.send(0x0700, cat(u16(m.Serial), []byte{byte(cmd)}, gbFrame(0x55, 0x7A, cmd, data)),
		fmt.Sprintf("%02XH %s %d 字节", cmd, recNames[cmd], len(data)))
}

func (t *Terminal) h8701(m *Msg) {
	r := newReader(m.Body)
	cmd := r.u8()
	d := newReader(gbData(r.rest()))
	t.mu.Lock()
	switch cmd {
	case 0x82:
		t.Cfg.VIN, t.Cfg.Plate, t.Rec.Class = d.str(17), d.str(12), d.str(12)
	case 0x83:
		t.Rec.Install = d.bcd(6)
	case 0xC2:
		d.time6() // 模拟器使用系统时间
	case 0xC3:
		d.time6()
		t.Rec.Pulse = d.u16()
	case 0xC4:
		d.time6()
		t.Rec.Install = d.bcd(6)
		fmt.Sscan(d.bcd(4), &t.Rec.Initial)
	}
	t.markDirty()
	t.mu.Unlock()
	t.addInbox("recorder", fmt.Sprintf("行驶记录仪 %02XH %s", cmd, recNames[cmd]), "", 0, nil)
	if d.err != nil {
		t.ack(m, 2)
		return
	}
	t.ack(m, 0)
}
