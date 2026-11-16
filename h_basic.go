package main

// 参数 / 终端控制 / 属性 / 升级 / 位置类下行

import (
	"fmt"
	"strings"
	"time"
)

var responseHandlers = map[int]handler{}

func init() {
	handlers[0x8103] = (*Terminal).h8103
	handlers[0x8104] = (*Terminal).h8104
	handlers[0x8106] = (*Terminal).h8104
	handlers[0x8105] = (*Terminal).h8105
	handlers[0x8107] = (*Terminal).h8107
	handlers[0x8108] = (*Terminal).h8108
	handlers[0x8201] = (*Terminal).h8201
	handlers[0x8202] = (*Terminal).h8202
	handlers[0x8203] = (*Terminal).h8203
	handlers[0x8204] = func(t *Terminal, m *Msg) { t.ack(m, 0) }
	responseHandlers[0x8003] = (*Terminal).h8003
	responseHandlers[0x8004] = (*Terminal).h8004
	responseHandlers[0x8800] = (*Terminal).h8800
}

func (t *Terminal) h8103(m *Msg) {
	r := newReader(m.Body)
	n := r.u8()
	var changed []string
	t.mu.Lock()
	for i := 0; i < n && r.err == nil; i++ {
		id := r.u32()
		raw := r.take(r.u8())
		if r.err != nil {
			break
		}
		v := decodeParam(id, raw)
		if t.Cfg.yue() {
			if id == 0xF370 { // 粤标：只支持获取，不支持设置
				changed = append(changed, "F370（只读，忽略）")
				continue
			}
			if gdParamIndex[id] != nil && gdParamIndex[id].Schema != nil {
				v = gdMerge(id, t.Params[id], raw) // 0xFF… 字段不修改
			}
		}
		t.applyParam(id, v)
		changed = append(changed, fmt.Sprintf("%04X=%s", id, v))
	}
	t.markDirty()
	t.mu.Unlock()
	if r.err != nil {
		t.ack(m, 2)
		return
	}
	t.addInbox("param", "设置参数", strings.Join(changed, "  "), 0, nil)
	t.ack(m, 0)
}

// applyParam 保存参数并同步联动字段。调用方持有 t.mu
func (t *Terminal) applyParam(id int, v string) {
	t.Params[id] = v
	switch id {
	case 0x0080:
		t.Mileage = float64(t.Params.Int(id)) / 10
	case 0x0083:
		t.Cfg.Plate = v
	case 0x0084:
		t.Cfg.PlateColor = t.Params.Int(id)
	}
	t.markDirty()
}

// h8104 8104 查询全部参数 / 8106 查询指定参数 -> 0104
func (t *Terminal) h8104(m *Msg) {
	var ids []int
	if m.ID == 0x8106 {
		r := newReader(m.Body)
		n := r.u8()
		for i := 0; i < n && r.err == nil; i++ {
			ids = append(ids, r.u32())
		}
	}
	t.mu.Lock()
	n, items := t.Params.items(ids)
	t.mu.Unlock()
	t.send(0x0104, cat(u16(m.Serial), []byte{byte(n)}, items), fmt.Sprintf("参数 %d 项", n))
}

var ctrlNames = map[int]string{1: "无线升级", 2: "控制连接指定服务器", 3: "关机", 4: "复位", 5: "恢复出厂设置", 6: "关闭数据通信", 7: "关闭所有无线通信"}

func (t *Terminal) h8105(m *Msg) {
	r := newReader(m.Body)
	cmd := r.u8()
	arg := ungbk(r.rest())
	t.addInbox("control", "终端控制："+ctrlNames[cmd], arg, 0, nil)
	if ctrlNames[cmd] == "" {
		t.ack(m, 3)
		return
	}
	t.ack(m, 0)
	f := strings.Split(arg, ";")
	get := func(i int) string {
		if i < len(f) {
			return strings.TrimSpace(f[i])
		}
		return ""
	}
	switch cmd {
	case 1: // URL;拨号点名称;拨号用户名;拨号密码;地址;TCP端口;UDP端口;制造商ID;硬件版本;固件版本;连接时限
		t.mu.Lock()
		yue := t.Cfg.yue()
		t.mu.Unlock()
		if yue { // 粤标：按升级文件名确定升级对象，上报 0x1FC4 进度
			go t.gdUpgrade(m.Serial, get(0), get(8), get(9))
		} else {
			go t.wirelessUpgrade(get(0), get(9))
		}
	case 2: // 连接控制;鉴权码;拨号点;用户名;密码;地址;TCP端口;UDP端口;连接时限
		t.mu.Lock()
		if get(0) == "1" {
			t.hostOver, t.portOver = "", 0
		} else if get(5) != "" {
			t.hostOver = get(5)
			fmt.Sscan(get(6), &t.portOver)
			if get(1) != "" {
				t.Cfg.Token = get(1)
			}
		}
		t.mu.Unlock()
		t.logf("控制连接：%s，1 秒后重连", map[bool]string{true: "切回原服务器", false: "切换到 " + get(5) + ":" + get(6)}[get(0) == "1"])
		time.AfterFunc(time.Second, t.dropConn)
	case 3, 6, 7:
		time.AfterFunc(time.Second, func() {
			t.mu.Lock()
			t.powerOff = cmd == 3
			t.mu.Unlock()
			t.Stop(ctrlNames[cmd])
		})
	case 4:
		time.AfterFunc(time.Second, func() { t.reboot(false) })
	case 5:
		time.AfterFunc(time.Second, func() { t.reboot(true) })
	}
}

// dropConn 断开当前连接，由会话循环按自动重连重新上线
func (t *Terminal) dropConn() {
	t.mu.Lock()
	c := t.conn
	t.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// reboot 复位：清运行态报警并重连；factory=true 恢复出厂（清鉴权码、参数、区域、电话本等）
func (t *Terminal) reboot(factory bool) {
	t.mu.Lock()
	t.alarmMan, t.alarmAuto, t.alarmClr, t.videoMan, t.pendEvt = 0, 0, 0, 0, 0
	t.contDrive, t.parkSec, t.tempUntil = 0, 0, time.Time{}
	if factory {
		t.Params = defaultParams()
		t.Cfg.Token = ""
		t.Areas, t.Phonebook, t.Inbox = nil, nil, nil
		t.Events, t.Menus = map[int]string{}, map[int]string{}
		t.hostOver, t.portOver = "", 0
		t.fillDefaults()
	}
	t.markDirty()
	want := t.want
	t.mu.Unlock()
	t.logf("终端%s，重新上线", map[bool]string{true: "恢复出厂设置", false: "复位"}[factory])
	if want {
		t.Stop("复位")
		time.Sleep(3 * time.Second)
		t.Start()
	}
}
