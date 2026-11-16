package main

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

// udpPlat 测试用 UDP 平台：一个数据报一条消息；可丢弃指定消息的首个应答以验证 UDP 超时重传
type udpPlat struct {
	pc       *net.UDPConn
	mu       sync.Mutex
	got      []*Msg
	dropOnce map[int]bool
	bad      int // 一个数据报内不止一帧
}

func newUDPPlat(t *testing.T) *udpPlat {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := &udpPlat{pc: pc, dropOnce: map[int]bool{}}
	t.Cleanup(func() { pc.Close() })
	go p.loop()
	return p
}

func (p *udpPlat) loop() {
	buf := make([]byte, 65536)
	serial := 0
	for {
		n, addr, err := p.pc.ReadFromUDP(buf)
		if err != nil {
			return
		}
		d := buf[:n]
		if n < 2 || d[0] != 0x7e || d[n-1] != 0x7e || bytes.Count(d, []byte{0x7e}) != 2 {
			p.mu.Lock()
			p.bad++
			p.mu.Unlock()
			continue
		}
		m := Parse(d[1 : n-1])
		if m == nil || !m.OK {
			continue
		}
		p.mu.Lock()
		p.got = append(p.got, m)
		drop := p.dropOnce[m.ID]
		delete(p.dropOnce, m.ID)
		p.mu.Unlock()
		if drop {
			continue
		}
		serial++
		var reply []byte
		if m.ID == 0x0100 {
			reply = Frame(0x8100, m.Phone, serial, cat(u16(m.Serial), []byte{0}, []byte("UDPTOKEN")), m.Version, 0, 0)
		} else {
			reply = Frame(0x8001, m.Phone, serial, cat(u16(m.Serial), u16(m.ID), []byte{0}), m.Version, 0, 0)
		}
		p.pc.WriteToUDP(reply, addr)
	}
}

func (p *udpPlat) count(id int) (n int, serials []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.got {
		if m.ID == id {
			n++
			serials = append(serials, m.Serial)
		}
	}
	return
}

func TestUDPTransport(t *testing.T) {
	stateDir = t.TempDir()
	p := newUDPPlat(t)
	port := p.pc.LocalAddr().(*net.UDPAddr).Port
	term := NewTerminal(Config{Host: "127.0.0.1", Port: port, Phone: "13800000077", Version: "2019"})
	if err := term.Do("setConfig", map[string]any{"transport": "udp"}); err != nil {
		t.Fatal(err)
	}
	if err := term.Do("setConfig", map[string]any{"transport": "sctp"}); err == nil {
		t.Fatal("非法 transport 应报错")
	}
	term.mu.Lock()
	term.Params[0x0002], term.Params[0x0003] = "30", "0" // TCP 参数不应生效
	term.Params[0x0004], term.Params[0x0005] = "1", "2"  // UDP：1s 超时，重传 2 次
	term.mu.Unlock()
	term.Start()
	defer term.Stop("测试结束")
	deadline := time.Now().Add(5 * time.Second)
	for term.State()["conn"].(ConnView).Status != "online" {
		if time.Now().After(deadline) {
			t.Fatal("UDP 未上线")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if c := term.State()["conn"].(ConnView); c.Token != "UDPTOKEN" || c.Transport != "udp" {
		t.Fatalf("%+v", c)
	}
	// 在线时不能切换传输方式
	if err := term.Do("setConfig", map[string]any{"transport": "tcp"}); err == nil {
		t.Fatal("在线时修改 transport 应报错")
	}
	p.mu.Lock()
	p.dropOnce[0x0200] = true
	p.mu.Unlock()
	if err := term.Do("report", nil); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(4 * time.Second)
	for {
		n, serials := p.count(0x0200)
		if n >= 2 {
			if serials[0] != serials[1] {
				t.Fatalf("重传应使用相同流水号 %v", serials)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("未按 0x0004 超时重传 0200（收到 %d 次）", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	term.mu.Lock()
	pending, retrans := len(term.pending), term.counters.Retrans
	term.mu.Unlock()
	if retrans < 1 || pending != 0 {
		t.Fatalf("retrans=%d pending=%d", retrans, pending)
	}
	p.mu.Lock()
	bad := p.bad
	p.mu.Unlock()
	if bad != 0 {
		t.Fatalf("%d 个数据报不是单条消息", bad)
	}
	if d := func() time.Duration { term.mu.Lock(); defer term.mu.Unlock(); return term.ackTimeout(2) }(); d != 2*time.Second {
		t.Fatalf("UDP 第 2 次重传超时应为 2×T0，得到 %v", d)
	}
}
