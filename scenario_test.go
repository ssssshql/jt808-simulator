package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseScenarioExample(t *testing.T) {
	sc, err := ParseScenario(scenarioExample)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Name != "示例-超速与断线重连" || !sc.Connect || sc.Route == nil || len(sc.Route.Points) != 5 || len(sc.Steps) != 9 {
		t.Fatalf("%+v", sc)
	}
	if b, _ := stepBit("alarm", sc.Steps[1].Params["bit"]); b != 1 {
		t.Fatalf("按名称取报警位: %d", b)
	}
	if *sc.Steps[1].AtPoint != 2 || sc.Steps[3].Params["wait"].(float64) != 30 {
		t.Fatalf("%+v", sc.Steps[3])
	}
	if !strings.Contains(sc.Steps[3].label(), "断线后等待重连") {
		t.Fatal(sc.Steps[3].label())
	}
	// JSON 同样可用
	j, err := ParseScenario(`{"name":"j","steps":[{"at":1,"action":"setAlarm","bit":2,"on":1}]}`)
	if err != nil || j.Steps[0].Action != "setAlarm" {
		t.Fatal(err)
	}
}

func TestParseScenarioErrors(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"name: x\nsteps:\n  - {at: 1}", "缺少 action"},
		{"name: x\nsteps:\n  - {at: 1, action: fly}", "未知动作"},
		{"name: x\nsteps:\n  - {at: -1, action: report}", "非负"},
		{"name: x\nsteps:\n  - {at: 1, after: 2, action: report}", "只能选一个"},
		{"name: x\nsteps:\n  - {action: alarm, bit: 99}", "bit 无效"},
		{"name: x\nsteps:\n  - {action: send}", "msgId"},
		{"name: x\nroute: {points: [[113, 23]]}", "至少 2"},
		{"name: x\nroute: {points: [[0, 0], [1, 1]]}", "第 1 点无效"},
		{"name: x", "没有任何步骤"},
		{"name: [x", "YAML"},
	} {
		if _, err := ParseScenario(c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v（期望含 %q）", c.text, err, c.want)
		}
	}
}

// 端到端：上线 -> 报警置位（0200 带报警位）-> 断线等待重连 -> 发送 0900 -> 清除；进度与日志正确
func TestScenarioRun(t *testing.T) {
	p := newFakePlat(t, V2019)
	stateDir = t.TempDir()
	m := NewManager(Config{Host: "127.0.0.1", Port: p.port(), Phone: "13800000001"})
	defer stopAll(m)
	text := `name: 端到端
connect: true
steps:
  - {after: 0.1, action: alarm, bit: 超速报警}
  - {after: 0.1, action: reconnect, wait: 0.3}
  - {after: 0, action: waitOnline, timeout: 10}
  - {after: 0.1, action: send, msgId: "0900", params: {type: 240, content: HI}}
  - {after: 0, action: clearAlarms}
`
	sc, err := ParseScenario(text)
	if err != nil {
		t.Fatal(err)
	}
	term, err := scenarios.Run(m, sc, "")
	if err != nil {
		t.Fatal(err)
	}
	p.expect(0x0200, 5*time.Second, func(m *Msg) bool { return len(m.Body) >= 4 && binary.BigEndian.Uint32(m.Body)&2 != 0 })
	m0900 := p.expect(0x0900, 10*time.Second, nil)
	if !bytes.Equal(m0900.Body, []byte{0xF0, 'H', 'I'}) {
		t.Fatalf("0900 %x", m0900.Body)
	}
	waitFor(t, 5*time.Second, func() bool { v := scenarios.Views(m); return len(v) == 1 && v[0].Status == "done" })
	v := scenarios.Views(m)[0]
	if v.Terminal != term.ID || v.Progress != 1 || v.Failures != 0 || len(v.Steps) != 5 {
		t.Fatalf("%+v", v)
	}
	p.mu.Lock()
	conns := p.conns
	p.mu.Unlock()
	if conns < 2 {
		t.Fatalf("应重连一次，连接数 %d", conns)
	}
	term.mu.Lock()
	alarm := term.alarmMan
	term.mu.Unlock()
	if alarm != 0 {
		t.Fatalf("报警未清除 %x", alarm)
	}
}

// 轨迹 + atPoint 触发 + 停止
func TestScenarioRouteAndStop(t *testing.T) {
	stateDir = t.TempDir()
	m := NewManager(Config{Phone: "13800000001"})
	defer stopAll(m)
	text := `name: 轨迹
route: {mult: 100, points: [[113.30, 23.10, 120], [113.301, 23.10, 120], [113.302, 23.10, 120], [113.303, 23.10, 120]]}
steps:
  - {atPoint: 3, action: status, bit: 13}
  - {after: 60, action: report}
`
	sc, err := ParseScenario(text)
	if err != nil {
		t.Fatal(err)
	}
	term, err := scenarios.Run(m, sc, "1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scenarios.Run(m, sc, "1"); err == nil {
		t.Fatal("同一终端不应同时运行两个场景")
	}
	waitFor(t, 10*time.Second, func() bool { term.mu.Lock(); defer term.mu.Unlock(); return term.StatusOn&(1<<13) != 0 })
	term.mu.Lock()
	mode := term.motion.Mode
	term.mu.Unlock()
	if mode != "track" {
		t.Fatalf("运动模式 %s", mode)
	}
	if !scenarios.Stop("1") {
		t.Fatal("停止失败")
	}
	if v := scenarios.Views(m)[0]; v.Status != "stopped" || v.Step != 1 {
		t.Fatalf("%+v", v)
	}
}

func TestScenarioAPI(t *testing.T) {
	stateDir = t.TempDir()
	scenarios = &ScenarioHub{runs: map[string]*scenRun{}}
	m := NewManager(Config{Phone: "13800000001"})
	defer stopAll(m)
	srv := httptest.NewServer(newWebMux(m))
	defer srv.Close()
	post := func(body map[string]any) map[string]any {
		b, _ := json.Marshal(body)
		r, err := srv.Client().Post(srv.URL+"/api/scenarios", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var j map[string]any
		json.NewDecoder(r.Body).Decode(&j)
		return j
	}
	if j := post(map[string]any{"op": "save", "text": scenarioExample}); j["ok"] != true {
		t.Fatal(j)
	}
	if j := post(map[string]any{"op": "save", "text": "name: bad\nsteps: [{action: nope}]"}); j["ok"] == true {
		t.Fatal("无效场景不应保存")
	}
	// 重新加载（模拟重启）
	scenarios = &ScenarioHub{runs: map[string]*scenRun{}}
	lib := scenarios.Library()
	if len(lib) != 1 || lib[0].Name != "示例-超速与断线重连" {
		t.Fatalf("%+v", lib)
	}
	if j := post(map[string]any{"op": "validate", "text": scenarioExample}); j["ok"] != true || len(j["steps"].([]any)) != 9 {
		t.Fatal(j)
	}
	if j := post(map[string]any{"op": "run", "text": "name: n\nterminal: new\nsteps: [{after: 30, action: report}]", "terminal": "1"}); j["ok"] != true || j["terminal"] != "2" {
		t.Fatal(j)
	}
	if j := post(map[string]any{"op": "stop", "terminal": "2"}); j["ok"] != true {
		t.Fatal(j)
	}
	if j := post(map[string]any{"op": "delete", "name": "示例-超速与断线重连"}); j["ok"] != true || len(scenarios.Library()) != 0 {
		t.Fatal(j)
	}
}
