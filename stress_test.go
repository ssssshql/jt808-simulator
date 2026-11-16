package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// multiPlat 多连接测试平台（TCP + UDP）：0100 回 8100，其它回 8001，并按需下发一条 8103 验证终端通用应答
type multiPlat struct {
	ln    net.Listener
	pc    *net.UDPConn
	conns atomic.Int64
	acks  atomic.Int64 // 收到虚拟终端的 0001
}

func newMultiPlat(t *testing.T) *multiPlat {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		pc, _ = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	}
	p := &multiPlat{ln: ln, pc: pc}
	t.Cleanup(func() { ln.Close(); pc.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.conns.Add(1)
			go p.serveTCP(c)
		}
	}()
	go p.serveUDP()
	return p
}

func (p *multiPlat) reply(m *Msg, serial int) [][]byte {
	switch m.ID {
	case 0x0100:
		return [][]byte{Frame(0x8100, m.Phone, serial, cat(u16(m.Serial), []byte{0}, []byte("TK"+m.Phone)), m.Version, 0, 0)}
	case 0x0001:
		p.acks.Add(1)
		return nil
	case 0x0102:
		return [][]byte{Frame(0x8001, m.Phone, serial, cat(u16(m.Serial), u16(m.ID), []byte{0}), m.Version, 0, 0),
			Frame(0x8104, m.Phone, serial+1000, nil, m.Version, 0, 0)} // 查询参数：虚拟终端应回 0001
	}
	return [][]byte{Frame(0x8001, m.Phone, serial, cat(u16(m.Serial), u16(m.ID), []byte{0}), m.Version, 0, 0)}
}

func (p *multiPlat) serveTCP(c net.Conn) {
	defer c.Close()
	var r FrameReader
	buf := make([]byte, 4096)
	serial := 0
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		for _, m := range r.Feed(buf[:n]) {
			serial++
			for _, f := range p.reply(m, serial) {
				c.Write(f)
			}
		}
	}
}

func (p *multiPlat) serveUDP() {
	buf := make([]byte, 4096)
	serial := 0
	for {
		n, addr, err := p.pc.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var r FrameReader
		for _, m := range r.Feed(buf[:n]) {
			serial++
			for _, f := range p.reply(m, serial) {
				p.pc.WriteToUDP(f, addr)
			}
		}
	}
}

func TestStressConfigValidate(t *testing.T) {
	ok := StressConfig{Host: "127.0.0.1", Port: 7611}
	if err := ok.normalize(); err != nil || ok.Count != 100 || ok.Transport != "tcp" || ok.PhoneStart != "19900000001" {
		t.Fatal(err, ok)
	}
	for _, c := range []StressConfig{
		{Port: 1}, {Host: "h", Port: 70000}, {Host: "h", Port: 1, Count: 2001}, {Host: "h", Port: 1, Ramp: 501},
		{Host: "h", Port: 1, Transport: "sctp"}, {Host: "h", Port: 1, PhoneStart: "99999", Count: 10},
		{Host: "h", Port: 1, Version: "2013", PhoneStart: "1234567890123"}, {Host: "h", Port: 1, Heartbeat: 1},
	} {
		if err := c.normalize(); err == nil {
			t.Fatalf("应报错: %+v", c)
		}
	}
	if stressPhone("00990", 15) != "01005" {
		t.Fatal(stressPhone("00990", 15))
	}
	if q := histQuantile([]int64{0, 0, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 0.95); q != 5 {
		t.Fatal(q)
	}
}

func runStress(t *testing.T, s *Stress, c StressConfig, want int) {
	t.Helper()
	if err := s.Start(c); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(c); err == nil {
		t.Fatal("重复启动应报错")
	}
	waitFor(t, 8*time.Second, func() bool { return s.online.Load() == int64(want) && s.acks.Load() >= int64(want*2) })
	s.Stop("测试")
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("停止后虚拟终端未退出")
	}
	v := s.View()
	st := v["stats"].(map[string]any)
	if st["regOK"].(int64) != int64(want) || st["timeouts"].(int64) != 0 || st["online"].(int64) != 0 || v["running"] != false {
		t.Fatalf("stats: %v err=%v", st, v["lastError"])
	}
}

func TestStressTCPAndUDP(t *testing.T) {
	p := newMultiPlat(t)
	port := p.ln.Addr().(*net.TCPAddr).Port
	before := runtime.NumGoroutine()
	s := &Stress{}
	runStress(t, s, StressConfig{Host: "127.0.0.1", Port: port, Count: 40, Ramp: 200, Interval: 1, PhoneStart: "13300000001"}, 40)
	if p.conns.Load() != 40 {
		t.Fatalf("TCP 连接数 %d", p.conns.Load())
	}
	if p.acks.Load() < 40 {
		t.Fatalf("虚拟终端应对 8104 回 0001，收到 %d", p.acks.Load())
	}
	udpPort := p.pc.LocalAddr().(*net.UDPAddr).Port
	runStress(t, s, StressConfig{Host: "127.0.0.1", Port: udpPort, Transport: "udp", Version: "2013", Count: 10, Ramp: 100, Interval: 1, PhoneStart: "013300000001"}, 10)
	time.Sleep(300 * time.Millisecond)
	if g := runtime.NumGoroutine(); g > before+10 {
		t.Fatalf("协程泄漏: %d -> %d", before, g)
	}
}

func TestStressAPI(t *testing.T) {
	stateDir = t.TempDir()
	m := NewManager(Config{Phone: "13800000001"})
	defer stopAll(m)
	h := newWebMux(m)
	post := func(body string) map[string]any {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/stress", bytes.NewReader([]byte(body))))
		var v map[string]any
		json.Unmarshal(w.Body.Bytes(), &v)
		return v
	}
	if r := post(`{"op":"start","host":"","port":1}`); r["ok"] != false {
		t.Fatal(r)
	}
	if r := post(`{"op":"start","host":"127.0.0.1","port":1,"count":1,"ackTimeout":1}`); r["ok"] != true {
		t.Fatal(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/stress", nil))
	var v map[string]any
	json.Unmarshal(w.Body.Bytes(), &v)
	if v["running"] != true || v["config"].(map[string]any)["count"] != 1.0 {
		t.Fatal(w.Body.String())
	}
	if r := post(`{"op":"stop"}`); r["ok"] != true {
		t.Fatal(r)
	}
	stress.wg.Wait()
}
