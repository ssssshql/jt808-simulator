package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBatchAddAndDeltaMotion(t *testing.T) {
	t.Cleanup(func() { stateDir = "state" })
	stateDir = t.TempDir()
	m := NewManager(Config{Phone: "13800001001", Plate: "粤A10001"})
	defer stopAll(m)

	base := m.Get("")
	added, err := m.AddBatch(base, true, 3)
	if err != nil {
		t.Fatalf("AddBatch failed: %v", err)
	}
	if len(added) != 3 {
		t.Fatalf("expected 3 terminals, got %d", len(added))
	}
	if added[2].phoneNow() != "13800001004" {
		t.Fatalf("unexpected phone %s", added[2].phoneNow())
	}

	if err := base.Do("setMotion", map[string]any{"mode": "delta", "dLng": 0.001, "dLat": 0.002, "dSpeed": 5, "dDir": 10}); err != nil {
		t.Fatalf("setMotion delta failed: %v", err)
	}
	base.mu.Lock()
	lng0, lat0 := base.Lng, base.Lat
	base.StatusOn |= 1
	base.stepMotion(1)
	lng1, lat1 := base.Lng, base.Lat
	base.mu.Unlock()
	if lng1 <= lng0 || lat1 <= lat0 {
		t.Fatalf("delta motion did not advance position: (%v,%v) -> (%v,%v)", lng0, lat0, lng1, lat1)
	}
}

func TestVersion2011AndHeiStd(t *testing.T) {
	term := NewTerminal(Config{Phone: "13800000001", Version: "2011", Maker: "AUTOT", Model: "M2011", DeviceID: "1234567", Plate: "黑A00001", SafetyStd: StdHB})
	if term.Cfg.ver() != V2013 {
		t.Fatalf("2011 should use V2013 frame format, got %d", term.Cfg.ver())
	}
	if !term.Cfg.hei() || !strings.Contains(term.Cfg.stdName(), "黑标") {
		t.Fatalf("expected 黑标 stdName, got %s", term.Cfg.stdName())
	}
	reg := term.bRegister()
	// 2011: prov(2)+city(2)+maker(5)+model(8)+dev(7)+color(1)+plate
	if len(reg) < 25 {
		t.Fatalf("2011 register body too short: %d", len(reg))
	}
}

func TestToolBuildAndParseAPI(t *testing.T) {
	mux := http.NewServeMux()
	registerToolAPI(mux)

	req := httptest.NewRequest("POST", "/api/tool/build", strings.NewReader(`{"version":2019,"msgId":"0004","phone":"13800000001","seq":7}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var bRes map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &bRes); err != nil || bRes["ok"] != true {
		t.Fatalf("build failed: %s", rec.Body.String())
	}

	pReq := httptest.NewRequest("POST", "/api/tool/parse", strings.NewReader(`{"hex":"`+bRes["hex"].(string)+`"}`))
	pRec := httptest.NewRecorder()
	mux.ServeHTTP(pRec, pReq)
	var pRes map[string]any
	if err := json.Unmarshal(pRec.Body.Bytes(), &pRes); err != nil || pRes["ok"] != true || pRes["crcOK"] != true {
		t.Fatalf("parse failed: %s", pRec.Body.String())
	}
}

func TestADPCM4AndJT809RoundTrip(t *testing.T) {
	term := NewTerminal(Config{Phone: "13800000001"})
	pcm := term.decodeADPCM4([]byte{0x12, 0x34, 0x56, 0x78})
	if len(pcm) != 16 {
		t.Fatalf("expected 16 bytes PCM, got %d", len(pcm))
	}

	body := build809BizBody(0x1202, "粤A18888", 2)
	frame := build809Frame(0x1200, 42, 101, true, 42, 1, 1, 1, body)
	if frame[0] != 0x5B || frame[len(frame)-1] != 0x5D {
		t.Fatalf("invalid 809 framing")
	}
	msg := parse809Frame(frame[1:len(frame)-1], 1, 1, 1)
	if msg == nil || !msg.CRCOK || msg.ID != 0x1200 || !bytes.Equal(msg.Body, body) {
		t.Fatalf("809 parse roundtrip failed: %+v", msg)
	}

	// Loopback Server <-> Client login handshake
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	j := &jt809Manager{cliStatus: "offline"}
	if err := j.startServer(port); err != nil {
		t.Fatalf("startServer: %v", err)
	}
	defer j.stopServer()

	cfg := jt809Config{Host: "127.0.0.1", Port: port, CenterID: 101, UserID: 101, Password: "123", Encrypt: true, M1: 1, IA1: 1, IC1: 1}
	if err := j.startClient(cfg); err != nil {
		t.Fatalf("startClient: %v", err)
	}
	defer j.stopClient()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		st := j.cliStatus
		j.mu.Unlock()
		if st == "online" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("jt809 client did not reach online state")
}
