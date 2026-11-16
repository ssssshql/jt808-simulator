package main

// 下行分发：分包重组（超时 0005 补传请求）/ 平台应答 / 指令按应答策略处理

import (
	"encoding/binary"
	"fmt"
	"sort"
	"time"
)

type partial struct {
	id, total, first int
	parts            map[int][]byte
	since            time.Time
	asks             int
	last             *Msg
}

type handler func(t *Terminal, m *Msg)

// handlers 在 init 中由各 h_*.go 注册
var handlers = map[int]handler{}

var resultText = map[int]string{0: "成功", 1: "失败", 2: "消息有误", 3: "不支持", 4: "报警处理确认"}

func (t *Terminal) onDown(m *Msg) {
	defer func() {
		if e := recover(); e != nil {
			t.errf("处理 %04X 异常: %v", m.ID, e)
		}
	}()
	t.mu.Lock()
	t.counters.Down++
	t.mu.Unlock()
	if !m.OK {
		t.logMsg("down", m.ID, m.Serial, len(m.Body), m.Raw, "校验码错误，丢弃")
		return
	}
	if m.Total > 1 {
		if m = t.assemble(m); m == nil {
			return
		}
	}
	if m.RSA { // 消息体 RSA 加密：用终端私钥解密（分包时对重组后的完整消息体解密）
		t.mu.Lock()
		key := t.termRSA()
		t.mu.Unlock()
		plain, err := rsaDecrypt(key, m.Body)
		if err != nil {
			t.logMsg("down", m.ID, m.Serial, len(m.Body), m.Raw, "RSA 解密失败: "+err.Error())
			t.ack(m, 2)
			return
		}
		dec := *m
		dec.Body, dec.RSA = plain, false
		m = &dec
		t.logf("%04X serial=%d 消息体已用终端私钥 RSA 解密（%d 字节明文）", m.ID, m.Serial, len(plain))
	}
	switch m.ID {
	case 0x8001:
		t.onCommonReply(m)
		return
	case 0x8100:
		t.logMsg("down", m.ID, m.Serial, len(m.Body), m.Raw, "")
		if len(m.Body) >= 2 {
			t.wake(waitKey(0x8100, int(binary.BigEndian.Uint16(m.Body))), m)
		}
		return
	}
	t.logMsg("down", m.ID, m.Serial, len(m.Body), m.Raw, "")
	if h := responseHandlers[m.ID]; h != nil { // 平台对终端请求的应答，不受应答策略影响
		h(t, m)
		return
	}
	t.mu.Lock()
	mode, delay := t.replyMode, t.replyDelay
	t.mu.Unlock()
	if mode == "none" {
		t.logf("应答策略=不应答，忽略 %04X（平台将超时）", m.ID)
		return
	}
	run := func() {
		defer func() {
			if e := recover(); e != nil {
				t.errf("处理 %04X 异常: %v", m.ID, e)
			}
		}()
		switch h := handlers[m.ID]; {
		case mode == "fail":
			t.ack(m, 1)
		case mode == "unsupported" || h == nil:
			t.ack(m, 3)
		default:
			h(t, m)
		}
	}
	if delay > 0 {
		time.AfterFunc(time.Duration(delay*float64(time.Second)), run)
	} else {
		go run()
	}
}

func (t *Terminal) wake(key string, m *Msg) bool {
	t.mu.Lock()
	ch := t.waiters[key]
	delete(t.waiters, key)
	t.mu.Unlock()
	if ch != nil {
		ch <- m
	}
	return ch != nil
}

func (t *Terminal) onCommonReply(m *Msg) {
	r := newReader(m.Body)
	serial, id, res := r.u16(), r.u16(), r.u8()
	summary := fmt.Sprintf("%04X %s", id, resultText[res])
	if r.err != nil {
		summary = "格式错误"
	}
	t.logMsg("down", m.ID, m.Serial, len(m.Body), m.Raw, summary)
	if r.err != nil {
		return
	}
	t.mu.Lock()
	p := t.pending[serial]
	if p != nil && p.id == id {
		delete(t.pending, serial)
	} else {
		p = nil
	}
	if res == 4 { // 报警处理确认：清除需人工确认的报警
		t.confirmAlarms(confirmMask)
	}
	t.mu.Unlock()
	if p != nil {
		p.done <- res
	}
}

// ack 终端通用应答
func (t *Terminal) ack(m *Msg, result int) {
	t.send(0x0001, bCommonReply(m.Serial, m.ID, result), fmt.Sprintf("%04X %s", m.ID, resultText[result]))
}

// assemble 下行分包重组；未收齐返回 nil。升级包每包单独应答
func (t *Terminal) assemble(m *Msg) *Msg {
	t.mu.Lock()
	p := t.downParts[m.ID]
	if p == nil || p.total != m.Total {
		p = &partial{id: m.ID, total: m.Total, first: (m.Serial - m.Index + 1) & 0xFFFF, parts: map[int][]byte{}}
		t.downParts[m.ID] = p
	}
	p.parts[m.Index] = m.Body
	p.since, p.last = time.Now(), m
	done := len(p.parts) == p.total
	if done {
		delete(t.downParts, m.ID)
	}
	t.mu.Unlock()
	t.logMsg("down", m.ID, m.Serial, len(m.Body), m.Raw, fmt.Sprintf("分包 %d/%d", m.Index, m.Total))
	if m.ID == 0x8108 && m.Index < m.Total {
		t.ack(m, 0)
	}
	if !done {
		return nil
	}
	var body []byte
	for i := 1; i <= p.total; i++ {
		body = append(body, p.parts[i]...)
	}
	t.logf("%04X 分包已收齐 %d 包，共 %d 字节", m.ID, p.total, len(body))
	full := *m
	full.Body, full.Total, full.Index = body, 0, 0
	return &full
}

// checkPartials 分包超时：2019 版发 0005 请求补传缺失包，最多 3 次
func (t *Terminal) checkPartials() {
	t.mu.Lock()
	var asks []*partial
	for k, p := range t.downParts {
		if time.Since(p.since) < 2*t.ackTimeout(0) {
			continue
		}
		if t.Cfg.ver() < V2019 || p.asks >= 3 {
			delete(t.downParts, k)
			t.errf("%04X 分包超时未收齐（%d/%d），丢弃", p.id, len(p.parts), p.total)
			continue
		}
		p.asks++
		p.since = time.Now()
		asks = append(asks, p)
	}
	t.mu.Unlock()
	for _, p := range asks {
		var miss []int
		for i := 1; i <= p.total; i++ {
			if _, ok := p.parts[i]; !ok {
				miss = append(miss, i)
			}
		}
		sort.Ints(miss)
		body := cat(u16(p.first), u16(len(miss)))
		for _, i := range miss {
			body = cat(body, u16(i))
		}
		t.send(0x0005, body, fmt.Sprintf("请求补传 %04X 缺失包 %v", p.id, miss))
	}
}
