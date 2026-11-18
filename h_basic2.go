package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// wirelessUpgrade 8105 命令 1：http(s) 地址实际下载（上限 50MB），其它协议模拟成功；结果 0108
func (t *Terminal) wirelessUpgrade(url, fw string) {
	result := 0
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		c := http.Client{Timeout: 60 * time.Second}
		resp, err := c.Get(url)
		if err == nil {
			n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 50<<20))
			resp.Body.Close()
			if resp.StatusCode != 200 {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			} else {
				t.logf("升级包下载完成 %d 字节", n)
			}
		}
		if err != nil {
			t.errf("升级包下载失败: %v", err)
			result = 1
		}
	} else {
		time.Sleep(3 * time.Second)
	}
	if result == 0 && fw != "" {
		t.mu.Lock()
		t.Cfg.FwVer = fw
		t.markDirty()
		t.mu.Unlock()
	}
	t.send(0x0108, []byte{0, byte(result)}, "终端升级 "+map[int]string{0: "成功", 1: "失败"}[result])
}

// h8107 查询终端属性 -> 0107
func (t *Terminal) h8107(m *Msg) {
	t.mu.Lock()
	c := t.Cfg
	t.mu.Unlock()
	did, model := 7, 20
	if c.ver() >= V2019 {
		did, model = 30, 30
	}
	iccid := bcd(c.ICCID, 10)
	// 终端类型：bit1 适用危险品车辆 bit2 普通货运 bit6 支持硬盘录像 ；GNSS：GPS+北斗；通信：GPRS+TD-LTE
	body := cat(u16(1<<1|1<<2|1<<6), fixed(c.Maker, 5), fixed(c.Model, model), fixed(c.DeviceID, did), iccid,
		lstr(c.HwVer), lstr(c.FwVer), []byte{0x03, 0x01 | 1<<6})
	t.send(0x0107, body, c.Model+" "+c.FwVer)
}

// upgradePkg 8108 消息体：升级类型 BYTE、制造商ID、版本号长度 BYTE + 版本号、升级数据包长度 DWORD、数据包
type upgradePkg struct {
	typ        int
	maker, ver string
	n          int
	data       []byte
	makerLen   int
}

// parse8108 制造商ID 2013 为 BYTE[5]、2019 为 BYTE[11]（与 0100 注册一致，业内实现均按 11 字节）。
// 先按版本对应长度解析，结构对不上（长度字段与剩余字节不符）时再试另一种，兼容按另一版本编码的平台。
func parse8108(version int, body []byte) (*upgradePkg, error) {
	lens := []int{5, 11}
	if version >= V2019 {
		lens = []int{11, 5}
	}
	var first *upgradePkg
	var firstErr error
	for _, ml := range lens {
		r := newReader(body)
		u := &upgradePkg{makerLen: ml}
		u.typ = r.u8()
		u.maker = r.str(ml)
		u.ver = r.lstr()
		u.n = r.u32()
		u.data = r.rest()
		ok := r.err == nil && len(u.data) == u.n
		if ok {
			return u, nil
		}
		if first == nil {
			first, firstErr = u, r.err
			if firstErr == nil {
				firstErr = fmt.Errorf("升级数据包长度 %d 与实际 %d 字节不符", u.n, len(u.data))
			}
		}
	}
	return first, firstErr
}

// h8108 升级包（分包已在 assemble 中逐包应答）
func (t *Terminal) h8108(m *Msg) {
	u, err := parse8108(m.Version, m.Body)
	t.ack(m, 0)
	result := 0
	if err != nil {
		t.errf("8108 升级包校验失败: %v", err)
		result = 1
	}
	typ, maker, ver, data, n := u.typ, u.maker, u.ver, u.data, u.n
	t.addInbox("upgrade", "升级包", fmt.Sprintf("类型=%d 制造商=%s(BYTE[%d]) 版本=%s %d/%d 字节", typ, maker, u.makerLen, ver, len(data), n), 0, nil)
	time.Sleep(2 * time.Second)
	if result == 0 && typ == 0 {
		t.mu.Lock()
		t.Cfg.FwVer = ver
		t.markDirty()
		t.mu.Unlock()
	}
	t.send(0x0108, []byte{byte(typ), byte(result)}, "升级结果 "+map[int]string{0: "成功", 1: "失败"}[result])
}

func (t *Terminal) h8201(m *Msg) {
	t.mu.Lock()
	body := cat(u16(m.Serial), t.locBody(nil))
	s := t.locSummary()
	t.mu.Unlock()
	t.send(0x0201, body, s)
}

// h8202 临时位置跟踪：间隔为 0 停止跟踪
func (t *Terminal) h8202(m *Msg) {
	r := newReader(m.Body)
	sec, valid := r.u16(), r.u32()
	if r.err != nil {
		t.ack(m, 2)
		return
	}
	t.mu.Lock()
	if sec == 0 {
		t.tempUntil = time.Time{}
	} else {
		t.tempInt, t.tempUntil, t.nextRpt = sec, time.Now().Add(time.Duration(valid)*time.Second), time.Now()
	}
	t.mu.Unlock()
	t.addInbox("track", "临时位置跟踪", fmt.Sprintf("间隔 %ds，有效期 %ds", sec, valid), 0, nil)
	t.ack(m, 0)
}

// h8203 人工确认报警：报警消息流水号(0=全部) + 确认报警类型
func (t *Terminal) h8203(m *Msg) {
	r := newReader(m.Body)
	r.u16()
	typ := uint32(r.u32())
	t.mu.Lock()
	// 确认类型 bit0 紧急 bit3 危险预警 bit20 进出区域 bit21 进出路线 bit22 路段时间 bit27 非法点火 bit28 非法位移
	t.confirmAlarms(typ)
	t.mu.Unlock()
	t.addInbox("alarm", "人工确认报警", fmt.Sprintf("类型=%08X", typ), 0, nil)
	t.ack(m, 0)
}

// h8003 补传分包请求
func (t *Terminal) h8003(m *Msg) {
	r := newReader(m.Body)
	first := r.u16()
	var n int
	if m.Version >= V2019 {
		n = r.u16()
	} else {
		n = r.u8()
	}
	var ids []int
	for i := 0; i < n && r.err == nil; i++ {
		ids = append(ids, r.u16())
	}
	t.logf("平台请求补传 serial=%d 的分包 %v，已补传 %d 包", first, ids, t.resend(first, ids))
}

func (t *Terminal) h8004(m *Msg) {
	r := newReader(m.Body)
	st := r.time6()
	if r.err == nil {
		t.logf("服务器时间 %s（本机偏差 %v）", st.Format("2006-01-02 15:04:05"), time.Since(st).Round(time.Second))
	}
}

// h8800 多媒体上传应答：无重传包表示上传完成
func (t *Terminal) h8800(m *Msg) {
	r := newReader(m.Body)
	mediaID := r.u32()
	if r.left() == 0 {
		t.logf("多媒体 %d 上传完成", mediaID)
		return
	}
	n := r.u8()
	var ids []int
	for i := 0; i < n && r.err == nil; i++ {
		ids = append(ids, r.u16())
	}
	if n == 0 {
		t.logf("多媒体 %d 上传完成", mediaID)
		return
	}
	first := t.findUpByMedia(mediaID)
	t.logf("平台请求重传多媒体 %d 的包 %v，已重传 %d 包", mediaID, ids, t.resend(first, ids))
}

// addInbox 记录平台下发内容（页面"收件箱"）
func (t *Terminal) addInbox(kind, title, content string, serial int, answers []Answer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inboxSeq++
	t.Inbox = append(t.Inbox, InboxItem{Seq: t.inboxSeq, T: time.Now().UnixMilli(), Kind: kind, Title: title,
		Content: content, Serial: serial, Answers: answers})
	if len(t.Inbox) > 200 {
		t.Inbox = t.Inbox[len(t.Inbox)-200:]
	}
	t.markDirty()
}
