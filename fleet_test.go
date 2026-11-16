package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFleet(t *testing.T) {
	p := newFakePlat(t, V2019)
	stateDir = t.TempDir()
	m := NewManager(Config{Host: "127.0.0.1", Port: p.port(), Phone: "13800000001"})
	defer stopAll(m)
	t2, err := m.Add(m.Get("1"), true)
	if err != nil {
		t.Fatal(err)
	}
	t1 := m.Get("1")
	t1.Start()
	waitFor(t, 5*time.Second, func() bool { t1.mu.Lock(); defer t1.mu.Unlock(); return t1.status == "online" })
	t2.Do("setAlarm", map[string]any{"bit": float64(1), "on": float64(1)})
	t2.Do("setVideoAlarm", map[string]any{"bit": float64(0), "on": float64(1)})

	h := newWebMux(m)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/fleet", nil))
	var v struct {
		Terminals []FleetItem `json:"terminals"`
		Total     int         `json:"total"`
		Online    int         `json:"online"`
		Alarming  int         `json:"alarming"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Total != 2 || v.Online != 1 || v.Alarming != 1 || v.Terminals[0].ID != "1" || v.Terminals[1].ID != "2" {
		t.Fatalf("%s", w.Body.String())
	}
	a, b := v.Terminals[0], v.Terminals[1]
	if a.Status != "online" || a.OnlineSince == 0 || a.Last == nil || a.LastUp == 0 || !a.Default || a.Phone != "13800000001" {
		t.Fatalf("%+v", a)
	}
	if b.Status != "offline" || len(b.Alarms) != 2 || b.Alarms[0] != "超速报警" || b.Plate != "粤A12346" {
		t.Fatalf("%+v", b)
	}
}
