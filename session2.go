package main

import (
	"encoding/hex"
	"fmt"
	"net"
	"time"
)

func (t *Terminal) bRegister() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.Cfg
	head := cat(u16(t.Params.Int(0x0081)), u16(t.Params.Int(0x0082)))
	plate := c.Plate
	if c.PlateColor == 0 {
		plate = c.VIN // 车牌颜色 0 时上报 VIN
	}
	if c.ver() >= V2019 {
		return cat(head, fixed(c.Maker, 11), fixed(c.Model, 30), fixed(c.DeviceID, 30), []byte{byte(c.PlateColor)}, gbk(plate))
	}
	return cat(head, fixed(c.Maker, 5), fixed(c.Model, 20), fixed(c.DeviceID, 7), []byte{byte(c.PlateColor)}, gbk(plate))
}

var regResult = map[byte]string{1: "车辆已被注册", 2: "数据库中无该车辆", 3: "终端已被注册", 4: "数据库中无该终端"}

// register 0100 -> 8100，超时按 0x0002/0x0003（UDP：0x0004/0x0005）重传
func (t *Terminal) register() error {
	t.mu.Lock()
	tries := t.maxRetries()
	var waits []time.Duration
	for i := 0; i <= tries; i++ {
		waits = append(waits, t.ackTimeout(i))
	}
	t.mu.Unlock()
	for i := 0; i <= tries; i++ {
		_, _, reply, err := t.sendX(0x0100, t.bRegister(), "", 0x8100)
		if err != nil {
			return err
		}
		select {
		case m := <-reply:
			r := newReader(m.Body)
			r.u16()
			res := r.u8()
			if r.err != nil {
				return fmt.Errorf("8100 格式错误")
			}
			if res != 0 {
				return fmt.Errorf("注册失败: %d %s（检查平台车辆/终端档案与 phone=%s）", res, regResult[byte(res)], t.Cfg.Phone)
			}
			t.mu.Lock()
			t.Cfg.Token = ungbk(r.rest())
			t.markDirty()
			t.mu.Unlock()
			t.logf("注册成功，鉴权码=%s", t.Cfg.Token)
			return nil
		case <-time.After(waits[i]):
			if i < tries {
				t.mu.Lock()
				t.counters.Retrans++
				t.mu.Unlock()
				t.logf("0100 注册应答超时，重传 %d/%d", i+1, tries)
			}
		}
	}
	return fmt.Errorf("注册无应答")
}

func (t *Terminal) auth() (int, error) {
	t.mu.Lock()
	c := t.Cfg
	t.mu.Unlock()
	body := gbk(c.Token)
	if c.ver() >= V2019 {
		body = cat(lstr(c.Token), fixed(c.IMEI, 15), fixed(c.FwVer, 20))
	}
	return t.sendWait(0x0102, body, "鉴权码="+c.Token)
}

func (t *Terminal) login() error {
	t.mu.Lock()
	tok := t.Cfg.Token
	t.mu.Unlock()
	if tok == "" {
		if err := t.register(); err != nil {
			return err
		}
	}
	r, err := t.auth()
	if err != nil {
		return err
	}
	if r != 0 {
		t.logf("鉴权失败 result=%d，清除鉴权码重新注册", r)
		t.mu.Lock()
		t.Cfg.Token = ""
		t.mu.Unlock()
		if err := t.register(); err != nil {
			return err
		}
		if r, err = t.auth(); err != nil {
			return err
		}
		if r != 0 {
			return fmt.Errorf("鉴权失败 result=%d", r)
		}
	}
	t.logf("鉴权成功，终端在线")
	return nil
}

// linkTick 每 500ms：超时重传；在线后另有心跳与下行分包超时检查
func (t *Terminal) linkTick(online bool) error {
	t.mu.Lock()
	hb := time.Duration(max(t.Params.Int(0x0001), 5)) * time.Second
	needHB := online && time.Since(t.lastUp) >= hb
	maxTry := t.maxRetries()
	now := time.Now()
	var resend []*pend
	var lost []*pend
	for k, p := range t.pending {
		if now.Before(p.deadline) {
			continue
		}
		if p.tries < maxTry {
			p.tries++
			p.deadline = now.Add(t.ackTimeout(p.tries))
			resend = append(resend, p)
		} else {
			delete(t.pending, k)
			lost = append(lost, p)
		}
	}
	t.counters.Retrans += len(resend)
	t.mu.Unlock()

	if needHB {
		t.send(0x0002, nil, "")
	}
	for _, p := range resend {
		t.logf("%04X serial=%d 应答超时，第 %d 次重传", p.id, p.serial, p.tries)
		if err := t.writeFrame(p.frame); err != nil {
			return err
		}
	}
	for _, p := range lost {
		close(p.done)
		t.errf("%04X serial=%d 重传 %d 次仍无应答", p.id, p.serial, maxTry)
		if p.id == 0x0200 {
			if m := Parse(p.frame[1 : len(p.frame)-1]); m != nil {
				t.storeBlind(m.Body)
			}
		}
		if p.id == 0x0002 {
			return fmt.Errorf("心跳无应答，判定链路断开")
		}
	}
	if online {
		t.checkPartials()
	}
	return nil
}

// ---------------- 盲区补报 ----------------

func (t *Terminal) storeBlind(body []byte) {
	t.mu.Lock()
	t.Blind = append(t.Blind, hex.EncodeToString(body))
	if len(t.Blind) > 5000 {
		t.Blind = t.Blind[len(t.Blind)-5000:]
	}
	t.markDirty()
	t.mu.Unlock()
}

// flushBlind 上线后以 0704 类型 1（盲区补报）批量补传，每批最多 10 条，收到成功应答再删除
func (t *Terminal) flushBlind() {
	for {
		t.mu.Lock()
		n := min(len(t.Blind), 10)
		batch := append([]string(nil), t.Blind[:n]...)
		t.mu.Unlock()
		if n == 0 {
			return
		}
		body := cat(u16(n), []byte{1})
		for _, h := range batch {
			b, _ := hex.DecodeString(h)
			body = cat(body, u16(len(b)), b)
		}
		r, err := t.sendWait(0x0704, body, fmt.Sprintf("盲区补报 %d 条", n))
		if err != nil || r != 0 {
			return
		}
		t.mu.Lock()
		t.Blind = t.Blind[min(n, len(t.Blind)):]
		t.markDirty()
		t.mu.Unlock()
		time.Sleep(200 * time.Millisecond)
	}
}

func (t *Terminal) readLoop(c net.Conn) error {
	var r FrameReader
	_, udp := c.(*net.UDPConn)
	buf := make([]byte, 65536)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return err
		}
		if udp { // UDP：一个数据报一条消息，不跨数据报拼接
			r = FrameReader{}
		}
		for _, m := range r.Feed(buf[:n]) {
			t.onDown(m)
		}
	}
}
