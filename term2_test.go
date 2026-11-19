package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestJT1078(t *testing.T) {
	p := newFakePlat(t, V2019)
	newTestTerm(t, p, "2019")
	p.send(0x9003, nil)
	m := p.expect(0x1003, 5*time.Second, nil)
	if !bytes.Equal(m.Body, []byte{6, 1, 0, 1, 1, 64, 1, 98, avChannels, avChannels}) {
		t.Fatalf("1003 %X", m.Body)
	}
	ms, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ms.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ms.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 20000)
		n, _ := io.ReadAtLeast(c, buf, 10000)
		got <- buf[:n]
	}()
	port := ms.Addr().(*net.TCPAddr).Port
	s := p.send(0x9101, cat(lstr("127.0.0.1"), u16(port), u16(0), []byte{1, 0, 0}))
	if p.ackResult(s) != 0 {
		t.Fatal("9101")
	}
	select {
	case b := <-got:
		// 30316364 81 M/PT ... SIM(10 BCD) 通道 1；首帧为 I 帧（数据类型 0）
		if !bytes.HasPrefix(b, []byte{0x30, 0x31, 0x63, 0x64, 0x81}) || b[6+2+10] != 1 || b[6+2+10+1]>>4 != 0 {
			t.Fatalf("RTP 头 %X", b[:30])
		}
		if b[5]&0x7F != 98 && b[5]&0x7F != 6 {
			t.Fatalf("PT %d", b[5]&0x7F)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到音视频流")
	}
	s = p.send(0x9205, cat([]byte{1}, bcdTime(time.Now().Add(-3*time.Hour)), bcdTime(time.Now()), make([]byte, 8), []byte{0, 0, 0}))
	m = p.reply(0x1205, s, 5*time.Second)
	if n := binary.BigEndian.Uint32(m.Body[2:]); n < 3 || len(m.Body) != 6+int(n)*28 {
		t.Fatalf("1205 n=%d len=%d", n, len(m.Body))
	}
	s = p.send(0x9102, []byte{1, 0, 0, 0})
	p.ackResult(s)
}

// fakeAttach 模拟附件服务器：第一次 1212 要求补传前 100 字节，第二次完成
func fakeAttach(t *testing.T, ver int, names chan string) int {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		serial := 0
		reply := func(id int, body []byte) {
			serial++
			c.Write(Frame(id, "13800000001", serial, body, ver, 0, 0))
		}
		var buf []byte
		tmp := make([]byte, 65536)
		asked := map[string]bool{}
		for {
			n, err := c.Read(tmp)
			if err != nil {
				return
			}
			buf = append(buf, tmp[:n]...)
			for len(buf) > 0 {
				if bytes.HasPrefix(buf, []byte{0x30, 0x31, 0x63, 0x64}) { // 文件数据码流
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
				if buf[0] != 0x7e {
					t.Errorf("附件流错位 %X", buf[:min(8, len(buf))])
					return
				}
				e := bytes.IndexByte(buf[1:], 0x7e)
				if e < 0 {
					break
				}
				m := Parse(buf[1 : e+1])
				buf = buf[e+2:]
				switch m.ID {
				case 0x1210, 0x1211:
					reply(0x8001, cat(u16(m.Serial), u16(m.ID), []byte{0}))
				case 0x1212:
					r := newReader(m.Body)
					name := r.lstr()
					typ := r.u8()
					if !asked[name] {
						asked[name] = true
						reply(0x9212, cat(lstr(name), []byte{byte(typ), 1, 1}, u32(0), u32(100)))
					} else {
						names <- name
						reply(0x9212, cat(lstr(name), []byte{byte(typ), 0, 0}))
					}
				}
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestAttachment(t *testing.T) {
	p := newFakePlat(t, V2019)
	term := newTestTerm(t, p, "2019")
	if err := term.Do("adas", map[string]any{"kind": "dsm", "type": float64(2), "level": float64(1), "files": float64(2)}); err != nil {
		t.Fatal(err)
	}
	m := p.expect(0x0200, 3*time.Second, func(m *Msg) bool { return findAttr(m.Body, 0x65) != nil })
	attr := findAttr(m.Body, 0x65)
	if len(attr) != 12+19+40 { // 报警ID..预留 12 + 车速..车辆状态 19 + 报警标识 40
		t.Fatalf("0x65 长度 %d", len(attr))
	}
	flag := attr[len(attr)-40:]
	if int(flag[30+6+1]) != 2 {
		t.Fatalf("附件数 %d", flag[37])
	}
	names := make(chan string, 4)
	port := fakeAttach(t, V2019, names)
	s := p.send(0x9208, cat(lstr("127.0.0.1"), u16(port), u16(0), flag, fixed("ALARM0001", 32), make([]byte, 16)))
	if p.ackResult(s) != 0 {
		t.Fatal("9208")
	}
	var got []string
	for len(got) < 2 {
		select {
		case n := <-names:
			got = append(got, n)
		case <-time.After(15 * time.Second):
			t.Fatalf("附件未上传完成 %v", got)
		}
	}
	if got[0] != "00_65_6502_0_ALARM0001.jpg" || got[1] != "02_65_6502_1_ALARM0001.mp4" {
		t.Fatalf("附件文件名 %v", got)
	}
}

func TestRetransBlindReset(t *testing.T) {
	p := newFakePlat(t, V2019)
	term := newTestTerm(t, p, "2019")
	// 平台不应答 0301：按 0x0002=1s 重传，流水号不变
	p.mu.Lock()
	p.noAck[0x0301] = true
	p.mu.Unlock()
	term.Do("send", map[string]any{"msgId": "0301", "params": map[string]any{"eventId": float64(3)}})
	a := p.expect(0x0301, 2*time.Second, nil)
	b := p.expect(0x0301, 4*time.Second, nil)
	if a.Serial != b.Serial {
		t.Fatalf("重传流水号变化 %d -> %d", a.Serial, b.Serial)
	}
	// 下行分包 8103：只发第 1 包，2019 版超时后请求补传 0005，再补发第 2 包
	body := cat([]byte{1}, u32(0x0029), []byte{4}, u32(7))
	first := p.sendFrame(0x8103, body[:5], 2, 1)
	m := p.expect(0x0005, 6*time.Second, nil)
	if int(binary.BigEndian.Uint16(m.Body)) != first || m.Body[2] != 1 || binary.BigEndian.Uint16(m.Body[3:]) != 2 {
		t.Fatalf("0005 %X", m.Body)
	}
	last := p.sendFrame(0x8103, body[5:], 2, 2)
	if p.ackResult(last) != 0 || term.Params.Int(0x0029) != 7 {
		t.Fatalf("分包 8103 未生效 %s", term.Params[0x0029])
	}
	// 应答策略
	for mode, want := range map[string]int{"fail": 1, "unsupported": 3} {
		term.Do("setReply", map[string]any{"mode": mode})
		if r := p.ackResult(p.send(0x8204, nil)); r != want {
			t.Fatalf("%s result=%d", mode, r)
		}
	}
	term.Do("setReply", map[string]any{"mode": "auto"})
	// 盲区：平台断开连接，期间位置进盲区；重连后 0704 类型 1 补报
	setP(term, 0x0029, "1")
	p.mu.Lock()
	p.conn.Close()
	p.mu.Unlock()
	waitFor(t, 6*time.Second, func() bool { term.mu.Lock(); defer term.mu.Unlock(); return len(term.Blind) >= 1 })
	m = p.expect(0x0704, 15*time.Second, nil)
	if m.Body[2] != 1 || binary.BigEndian.Uint16(m.Body) < 1 {
		t.Fatalf("0704 %X", m.Body[:3])
	}
	waitFor(t, 3*time.Second, func() bool { term.mu.Lock(); defer term.mu.Unlock(); return len(term.Blind) == 0 })
	// 8105 复位：断开后用原鉴权码重连（不再注册）
	p.mu.Lock()
	conns := p.conns
	p.mu.Unlock()
	s := p.send(0x8105, []byte{4})
	p.ackResult(s)
	p.expect(0x0102, 15*time.Second, nil)
	p.mu.Lock()
	if p.conns <= conns {
		t.Fatal("复位后未重连")
	}
	p.mu.Unlock()
	// 8800 要求重传多媒体分包
	term.Do("photo", map[string]any{"channel": float64(1)})
	pk := p.expect(0x0801, 8*time.Second, nil)
	mid := int(binary.BigEndian.Uint32(pk.Body))
	for len(p.pkts) > 0 {
		<-p.pkts
	}
	p.send(0x8800, cat(u32(mid), []byte{1}, u16(2)))
	select {
	case r := <-p.pkts:
		if r.Index != 2 {
			t.Fatalf("重传包序号 %d", r.Index)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("8800 未重传")
	}
	if !strings.Contains(term.State()["conn"].(ConnView).Status, "online") {
		t.Fatal("应在线")
	}
}

// findAttr 解析 0200 附加信息
func findAttr(body []byte, id int) []byte {
	for p := 28; p+2 <= len(body); {
		n := int(body[p+1])
		if p+2+n > len(body) {
			return nil
		}
		if int(body[p]) == id {
			return body[p+2 : p+2+n]
		}
		p += 2 + n
	}
	return nil
}

// 8606 路线：路段属性 bit0 行驶时间，过长阈值 1s；沿路线行驶驶离路段后报警位 bit22 + 附加信息 0x13（结果 1 过长）
func TestRouteSegmentTime(t *testing.T) {
	p := newFakePlat(t, V2019)
	term := newTestTerm(t, p, "2019")
	lat0, lng0 := term.Lat, term.Lng
	pt := func(i int) (float64, float64) { return moveBy(lat0, lng0, 90, float64(i)*80) }
	var pts []byte
	for i := 0; i < 3; i++ {
		lat, lng := pt(i)
		pts = cat(pts, u32(i+1), u32(101+i), u32(int(lat*1e6)), u32(int(lng*1e6)), []byte{20})
		if i < 2 {
			pts = cat(pts, []byte{1}, u16(1), u16(0)) // 路段行驶时间：过长 1s，不足 0
		} else {
			pts = cat(pts, []byte{0})
		}
	}
	body := cat(u32(9), u16(0), u16(3), pts, u16(len(gbk("测试路线"))), gbk("测试路线"))
	if p.ackResult(p.send(0x8606, body)) != 0 {
		t.Fatal("8606")
	}
	term.mu.Lock()
	if len(term.Areas) != 1 || len(term.Areas[0].Segs) != 3 || term.Areas[0].Segs[0].Max != 1 {
		term.mu.Unlock()
		t.Fatalf("路段解析 %+v", term.Areas)
	}
	term.mu.Unlock()
	term.Do("setMotion", map[string]any{"mode": "route", "speed": float64(60)})
	m := p.expect(0x0200, 20*time.Second, func(m *Msg) bool {
		return binary.BigEndian.Uint32(m.Body)&(1<<22) != 0 && findAttr(m.Body, 0x13) != nil
	})
	a := findAttr(m.Body, 0x13)
	if len(a) != 7 || binary.BigEndian.Uint32(a) != 101 || a[6] != 1 || binary.BigEndian.Uint16(a[4:]) < 2 {
		t.Fatalf("0x13 %X", a)
	}
	term.Do("setMotion", map[string]any{"mode": "stop"})
	// 8203 确认后清除
	p.ackResult(p.send(0x8203, cat(u16(0), u32(1<<22))))
	if term.alarmWord()&(1<<22) != 0 {
		t.Fatal("bit22 未清除")
	}
}

// RSA：8A00 平台公钥 -> 0A00 终端公钥；下行 bit10 加密消息体用终端私钥解密；开启上行加密后用平台公钥加密
func TestRSA(t *testing.T) {
	p := newFakePlat(t, V2019)
	term := newTestTerm(t, p, "2019")
	pk, _ := rsa.GenerateKey(rand.Reader, 1024)
	p.send(0x8A00, cat(u32(pk.E), pk.N.FillBytes(make([]byte, 128))))
	m := p.expect(0x0A00, 5*time.Second, nil)
	if m.RSA || len(m.Body) != 132 {
		t.Fatalf("0A00 %d 字节 rsa=%v", len(m.Body), m.RSA)
	}
	termPub := &rsa.PublicKey{E: int(binary.BigEndian.Uint32(m.Body)), N: new(big.Int).SetBytes(m.Body[4:])}
	plain := cat([]byte{1}, u32(0x0029), []byte{4}, u32(17))
	c, err := rsaEncrypt(termPub, plain)
	if err != nil {
		t.Fatal(err)
	}
	if r := p.ackResult(p.sendEnc(0x8103, c)); r != 0 || term.Params.Int(0x0029) != 17 {
		t.Fatalf("加密 8103 result=%d 0029=%s", r, term.Params[0x0029])
	}
	if r := p.ackResult(p.sendEnc(0x8103, []byte{1, 2, 3})); r != 2 {
		t.Fatalf("无法解密应回 2，实际 %d", r)
	}
	if err := term.Do("setConfig", map[string]any{"upRsa": float64(1)}); err != nil {
		t.Fatal(err)
	}
	term.Do("send", map[string]any{"msgId": "0301", "params": map[string]any{"eventId": float64(5)}})
	m = p.expect(0x0301, 5*time.Second, nil)
	if !m.RSA || len(m.Body) != 128 {
		t.Fatalf("0301 未加密 rsa=%v len=%d", m.RSA, len(m.Body))
	}
	if got, err := rsaDecrypt(pk, m.Body); err != nil || !bytes.Equal(got, []byte{5}) {
		t.Fatalf("平台解密 %X %v", got, err)
	}
	// 终端私钥随状态保存：重新加载后公钥不变
	term.mu.Lock()
	saved := term.RSAKey
	term.rsaKey = nil
	k2 := term.termRSA()
	term.mu.Unlock()
	if saved == "" || k2.N.Cmp(termPub.N) != 0 {
		t.Fatal("终端 RSA 密钥未持久化")
	}
}
