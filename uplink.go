package main

// 页面/命令行触发的上行报文

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// bDriver 0702 驾驶员身份：上班插卡 / 下班拔卡。调用方不持有 t.mu
func (t *Terminal) bDriver() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.Driver
	if !d.OnDuty {
		return cat([]byte{2}, bcdNow())
	}
	b := cat([]byte{1}, bcdNow(), []byte{0}, lstr(d.Name), fixed(d.LicenseNo, 20), lstr(d.Org), bcd(d.ValidTo, 4))
	if t.Cfg.ver() >= V2019 {
		b = cat(b, fixed(d.IDCard, 20))
	}
	return b
}

func pint(p map[string]any, k string, def int) int {
	switch v := p[k].(type) {
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	case bool:
		if v {
			return 1
		}
		return 0
	}
	return def
}

func pstr(p map[string]any, k, def string) string {
	switch v := p[k].(type) {
	case string:
		if v != "" {
			return v
		}
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return def
}

// sendUplink 按消息 ID 组包发送
func (t *Terminal) sendUplink(id int, p map[string]any) error {
	var body []byte
	summary := ""
	switch id {
	case 0x0200:
		t.reportNow()
		return nil
	case 0x0002:
	case 0x0004:
	case 0x0003:
		return t.logout()
	case 0x0301:
		body = []byte{byte(pint(p, "eventId", 1))}
		t.mu.Lock()
		summary = t.Events[pint(p, "eventId", 1)]
		t.mu.Unlock()
	case 0x0303:
		body = []byte{byte(pint(p, "type", 1)), byte(pint(p, "action", 1))}
		summary = map[int]string{0: "取消", 1: "点播"}[pint(p, "action", 1)]
	case 0x0108:
		body = []byte{byte(pint(p, "type", 0)), byte(pint(p, "result", 0))}
	case 0x0702:
		t.mu.Lock()
		t.Driver.OnDuty = pint(p, "status", 1) == 1
		d := &t.Driver
		d.Name, d.LicenseNo = pstr(p, "name", d.Name), pstr(p, "licenseNo", d.LicenseNo)
		d.Org, d.ValidTo = pstr(p, "org", d.Org), pstr(p, "validTo", d.ValidTo)
		t.markDirty()
		summary = map[bool]string{true: "插卡 " + d.Name, false: "拔卡"}[d.OnDuty]
		t.mu.Unlock()
		body = t.bDriver()
		if c := pint(p, "cardStatus", 0); c != 0 && body[0] == 1 {
			body = cat(body[:7], []byte{byte(c)}) // 读卡失败：只带结果
		}
	case 0x0704:
		n, typ := max(min(pint(p, "count", 5), 50), 1), pint(p, "type", 0)
		body = cat(u16(n), []byte{byte(typ)})
		t.mu.Lock()
		for i := 0; i < n; i++ {
			b := t.locBody(nil)
			body = cat(body, u16(len(b)), b)
		}
		t.mu.Unlock()
		summary = fmt.Sprintf("%d 条 %s", n, map[int]string{0: "正常批量", 1: "盲区补报"}[typ])
	case 0x0705:
		n := max(min(pint(p, "count", 3), 50), 1)
		body = cat(u16(n), bcd(time.Now().Format("150405")+fmt.Sprintf("%04d", time.Now().Nanosecond()/1e6), 5))
		for i := 0; i < n; i++ {
			rpm := 800 + rand.Intn(2000)
			body = cat(body, u32(0x0CF00400+i), []byte{0, 0, 0, byte(rpm * 8 >> 8), byte(rpm * 8), 0, 0, 0})
		}
		summary = fmt.Sprintf("%d 条 CAN", n)
	case 0x0800:
		t.mu.Lock()
		t.MediaSeq++
		mid := t.MediaSeq
		t.mu.Unlock()
		body = cat(u32(mid), []byte{byte(pint(p, "type", 0)), byte(pint(p, "format", 0)), byte(pint(p, "event", 0)), byte(pint(p, "channel", 1))})
	case 0x0801:
		t.mu.Lock()
		it := t.newMedia(pint(p, "type", 0), pint(p, "channel", 1), pint(p, "event", 0), 2, 5, 3)
		t.mu.Unlock()
		return t.uploadMedia(it)
	case 0x0900:
		content := []byte(pstr(p, "content", "HELLO"))
		if n := pint(p, "size", 0); n > 0 {
			content = bytes.Repeat([]byte{0x5A}, n)
		}
		body = cat([]byte{byte(pint(p, "type", 0xF0))}, content)
		summary = fmt.Sprintf("类型 0x%02X %d 字节", pint(p, "type", 0xF0), len(content))
	case 0x0901:
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		w.Write(bytes.Repeat([]byte(`{"seq":1,"speed":60,"note":"compressed report"}`), max(pint(p, "size", 20), 1)))
		w.Close()
		body = cat(u32(gz.Len()), gz.Bytes())
		summary = fmt.Sprintf("gzip %d 字节", gz.Len())
	case 0x0701:
		c := gbk(pstr(p, "content", "电子运单：广州 -> 深圳，货物 10 吨"))
		body = cat(u32(len(c)), c)
	case 0x1005:
		now := time.Now()
		body = cat(bcdTime(now.Add(-5*time.Minute)), bcdTime(now), u16(pint(p, "up", 3)), u16(pint(p, "down", 1)))
		summary = fmt.Sprintf("上车 %d 下车 %d", pint(p, "up", 3), pint(p, "down", 1))
	default:
		return fmt.Errorf("不支持的上行 %04X（可用 raw 发送任意报文）", id)
	}
	_, _, err := t.send(id, body, summary)
	return err
}

// logout 0003 注销：平台应答后清除鉴权码并下线
func (t *Terminal) logout() error {
	r, err := t.sendWait(0x0003, nil, "")
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.Cfg.Token = ""
	t.markDirty()
	t.mu.Unlock()
	t.Stop(fmt.Sprintf("注销（平台结果 %d），已清除鉴权码", r))
	return nil
}

// alarmPhotos 报警位上升沿触发拍照（0x0052 报警拍摄开关，0x0053 存储标志）。调用方持有 t.mu
func (t *Terminal) alarmPhotos(prev, cur uint32) {
	rise := cur &^ prev & uint32(t.Params.Int(0x0052))
	for bit, evt := range map[int]int{0: 2, 29: 3} { // 紧急报警 -> 抢劫报警触发，碰撞侧翻 -> 碰撞侧翻报警触发
		if rise&(1<<bit) != 0 {
			store := t.Params.Int(0x0053)&(1<<bit) != 0
			go t.capture(1, evt, store, 1)
		}
	}
}
