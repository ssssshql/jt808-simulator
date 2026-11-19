package main

// v1.5.0 新增后端行为的测试：接口令牌、状态推送（SSE 差量）、状态文件 .bak 回退、盲区队列上限、报警位锁定、
// 场景断言与命令行 -scenario

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebGuardAlwaysRequiresToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	do := func(h http.Handler, host, url string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		r.Host = host
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do(webGuard(ok, ""), "127.0.0.1:1", "/api/state", nil); w.Code != 403 {
		t.Fatalf("空令牌必须拒绝，得到 %d", w.Code)
	}
	g := webGuard(ok, "secret")
	if w := do(g, "127.0.0.1:1", "/api/state", nil); w.Code != 403 {
		t.Fatalf("无令牌应拒绝 %d", w.Code)
	}
	w := do(g, "127.0.0.1:1", "/?k=secret", nil)
	if w.Code != 200 || len(w.Result().Cookies()) == 0 {
		t.Fatalf("?k= 正确应放行并种 cookie: %d", w.Code)
	}
	if w := do(g, "localhost:1", "/api/state", w.Result().Cookies()[0]); w.Code != 200 {
		t.Fatalf("cookie 正确应放行 %d", w.Code)
	}
	if w := do(g, "evil.example:1", "/?k=secret", nil); w.Code != 403 {
		t.Fatalf("非回环 Host 应拒绝 %d", w.Code)
	}
}

func TestStateDiff(t *testing.T) {
	d, prev := stateDiff(nil, map[string]any{"a": 1, "b": []int{1, 2}})
	if len(d) != 2 {
		t.Fatalf("首次应返回全部字段 %v", d)
	}
	d, _ = stateDiff(prev, map[string]any{"a": 1, "b": []int{1, 3}, "c": "x"})
	if len(d) != 2 || d["b"] == nil || d["c"] == nil || d["a"] != nil {
		t.Fatalf("只应返回变化的字段 %v", d)
	}
}

// 状态推送：先推全量快照，页面操作后很快推送只含变化字段的差量
func TestStateStream(t *testing.T) {
	stateDir = t.TempDir()
	m := NewManager(Config{Phone: "13800000001"})
	defer stopAll(m)
	srv := httptest.NewServer(newWebMux(m))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/t/1/state/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type %s", ct)
	}
	events := make(chan map[string]json.RawMessage, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				var v map[string]json.RawMessage
				json.Unmarshal([]byte(line[6:]), &v)
				events <- v
			}
		}
		close(events)
	}()
	first := <-events
	var full map[string]json.RawMessage
	json.Unmarshal(first["state"], &full)
	if string(first["full"]) != "true" || full["params"] == nil || full["conn"] == nil || len(first["build"]) == 0 {
		t.Fatalf("首条应为全量快照: %s", first["full"])
	}
	b, _ := json.Marshal(map[string]any{"type": "setAlarm", "bit": 0, "on": 1})
	r, err := http.Post(srv.URL+"/api/t/1/action", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			var diff map[string]json.RawMessage
			json.Unmarshal(ev["diff"], &diff)
			if diff["params"] != nil {
				t.Fatal("参数未变化，不应推送")
			}
			if a := diff["alarms"]; a != nil && strings.Contains(string(a), `"bit":0,"name":"紧急报警","on":true`) {
				return
			}
		case <-deadline:
			t.Fatal("3 秒内未推送报警位变化")
		}
	}
}

func TestDurableBakFallback(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.json")
	if err := writeFileDurable(f, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := writeFileDurable(f, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f + ".bak"); string(b) != `{"v":1}` {
		t.Fatalf(".bak 应为上一份: %s", b)
	}
	if _, err := os.Stat(f + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("不应残留 .tmp")
	}
	var v struct{ V int }
	if used, err := readJSONDurable(f, &v); err != nil || used != f || v.V != 2 {
		t.Fatalf("正常读取 %s %v %d", used, err, v.V)
	}
	os.WriteFile(f, []byte(`{"v":`), 0o644) // 写到一半掉电
	v.V = 0
	if used, err := readJSONDurable(f, &v); err != nil || used != f+".bak" || v.V != 1 {
		t.Fatalf("损坏时应回退 .bak: %s %v %d", used, err, v.V)
	}
	// 损坏的正式文件不覆盖好的备份
	if err := writeFileDurable(f, []byte(`{"v":3}`)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f + ".bak"); string(b) != `{"v":1}` {
		t.Fatalf("坏文件不应轮换为 .bak: %s", b)
	}
	os.Remove(f)
	if used, err := readJSONDurable(f, &v); err != nil || used != f+".bak" {
		t.Fatalf("缺失时应回退 .bak: %s %v", used, err)
	}
	os.Remove(f + ".bak")
	if _, err := readJSONDurable(f, &v); !os.IsNotExist(err) {
		t.Fatalf("都不存在应返回 NotExist: %v", err)
	}

	// 终端状态：正式文件损坏时从 .bak 恢复
	stateDir = t.TempDir()
	term := NewTerminal(Config{Phone: "13900000009"})
	term.mu.Lock()
	term.Mileage = 111
	term.save()
	term.Mileage = 222
	term.save()
	term.mu.Unlock()
	os.WriteFile(term.stateFile(), []byte("{broken"), 0o644)
	again := NewTerminal(Config{Phone: "13900000009"})
	if again.Mileage != 111 {
		t.Fatalf("应从 .bak 恢复里程 111，得到 %v", again.Mileage)
	}
}

func TestBlindCap(t *testing.T) {
	stateDir = t.TempDir()
	term := NewTerminal(Config{Phone: "13900000010"})
	if term.Cfg.blindCap() != defaultBlindCap {
		t.Fatalf("默认上限 %d", term.Cfg.blindCap())
	}
	if err := term.Do("setConfig", map[string]any{"blindCap": 5}); err == nil {
		t.Fatal("过小的上限应报错")
	}
	if err := term.Do("setConfig", map[string]any{"blindCap": 10}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 15; i++ {
		term.storeBlind([]byte{byte(i)})
	}
	term.mu.Lock()
	n, dropped, first := len(term.Blind), term.blindDropped, term.Blind[0]
	term.mu.Unlock()
	if n != 10 || dropped != 5 || first != "05" {
		t.Fatalf("队列 %d 丢弃 %d 最旧 %s", n, dropped, first)
	}
	c := term.State()["counters"].(map[string]int)
	if c["blindCap"] != 10 || c["blindDropped"] != 5 {
		t.Fatalf("状态计数 %v", c)
	}
	if err := term.Do("setConfig", map[string]any{"blindCap": 0}); err != nil { // 0 = 默认
		t.Fatal(err)
	}
	term.Cfg.BlindCap = 10
	term.mu.Lock()
	term.Cfg.BlindCap = 10
	term.Blind = append(term.Blind, "aa", "bb")
	term.trimBlind()
	n = len(term.Blind)
	term.mu.Unlock()
	if n != 10 {
		t.Fatalf("调小上限后应裁剪到 10，得到 %d", n)
	}
}

func TestAlarmLock(t *testing.T) {
	stateDir = t.TempDir()
	term := NewTerminal(Config{Phone: "13900000011"})
	term.mu.Lock()
	term.setAuto(1, true) // 模拟器判定超速
	term.mu.Unlock()
	alarm := func() uint32 { term.mu.Lock(); defer term.mu.Unlock(); return term.alarmWord() }
	if alarm()&2 == 0 {
		t.Fatal("自动判定应置位")
	}
	off := false
	if err := term.lockAlarm(1, true, &off); err != nil {
		t.Fatal(err)
	}
	if alarm()&2 != 0 {
		t.Fatal("锁定为清除后自动判定不应生效")
	}
	for _, b := range term.State()["alarms"].([]Bit) {
		if b.Bit == 1 && (!b.Auto || !b.Locked || b.On) {
			t.Fatalf("超速位标记错误 %+v", b)
		}
		if b.Bit == 0 && (b.Auto || b.Locked) {
			t.Fatalf("紧急报警不是自动判定位 %+v", b)
		}
	}
	// 锁定后手动切换：改锁定值；平台确认不清除锁定位
	if err := term.setBit("setAlarm", 1, true); err != nil {
		t.Fatal(err)
	}
	term.mu.Lock()
	term.setAuto(1, false)
	term.confirmAlarms(0xFFFFFFFF)
	term.mu.Unlock()
	if alarm()&2 == 0 {
		t.Fatal("锁定为置位后应保持置位")
	}
	// 锁定保存在状态文件
	term.mu.Lock()
	term.save()
	term.mu.Unlock()
	if re := NewTerminal(Config{Phone: "13900000011"}); re.AlarmLock&2 == 0 || re.AlarmLockOn&2 == 0 {
		t.Fatal("锁定应持久化")
	}
	if err := term.Do("lockAlarm", map[string]any{"bit": 1, "lock": 0}); err != nil {
		t.Fatal(err)
	}
	term.mu.Lock()
	locked, man := term.AlarmLock, term.alarmMan
	term.mu.Unlock()
	if locked != 0 || man&2 == 0 {
		t.Fatalf("解锁后锁定值交还手动位 lock=%x man=%x", locked, man)
	}
	if err := term.Do("lockAlarm", map[string]any{"bit": 99}); err == nil {
		t.Fatal("无效位应报错")
	}
}

func TestParse8103(t *testing.T) {
	items, err := parse8103(cat([]byte{2}, u32(0x0001), []byte{4}, u32(60), u32(0x0083), []byte{2}, []byte("AB")))
	if err != nil || len(items) != 2 || items[0].id != 1 || decodeParam(1, items[0].raw) != "60" || string(items[1].raw) != "AB" {
		t.Fatalf("%+v %v", items, err)
	}
	if _, err := parse8103([]byte{2, 0, 0, 0, 1, 4, 0}); err == nil {
		t.Fatal("截断的参数项应报错")
	}
}

func TestScenarioAssertParse(t *testing.T) {
	for _, bad := range []string{
		"steps: [{action: expectDown}]",
		"steps: [{action: expectState, path: loc.speed}]",
		"steps: [{action: expectState, path: a, value: 1, op: '=~'}]",
		"steps: [{action: expectBit, kind: nope, bit: 1}]",
		"steps: [{action: expectBit, bit: 99}]",
		"steps: [{action: expectDown, msgId: '8103', since: later}]",
	} {
		if _, err := ParseScenario(bad); err == nil {
			t.Fatalf("应校验失败: %s", bad)
		}
	}
	if _, err := ParseScenario("steps: [{action: expectBit, kind: alarm, bit: 超速报警, on: true, within: 5}, {action: expectState, path: conn.status, value: online}]"); err != nil {
		t.Fatal(err)
	}
	st := map[string]any{"conn": ConnView{Status: "online"}, "loc": map[string]any{"speed": 61.5}, "alarms": []Bit{{Bit: 1, On: true}}}
	if v, ok := statePath(st, "loc.speed"); !ok || !compareVal(v, ">=", 60) || compareVal(v, "<", "60") {
		t.Fatalf("数值比较 %v", v)
	}
	if v, ok := statePath(st, "conn.status"); !ok || !compareVal(v, "==", "online") || !compareVal(v, "contains", "lin") {
		t.Fatalf("字符串比较 %v", v)
	}
	if v, ok := statePath(st, "alarms.0.on"); !ok || !compareVal(v, "==", true) {
		t.Fatalf("数组下标 %v", v)
	}
	if _, ok := statePath(st, "loc.nope"); ok {
		t.Fatal("不存在的字段")
	}
}

// 断言：收到下行 / 状态字段 / 位状态；失败的断言计入失败数
func TestScenarioAssertRun(t *testing.T) {
	p := newFakePlat(t, V2019)
	stateDir = t.TempDir()
	if !Quiet {
		Quiet = true
	}
	scenarios = &ScenarioHub{runs: map[string]*scenRun{}}
	m := &Manager{noSave: true, next: 2}
	term := m.spawn("1", Config{Host: "127.0.0.1", Port: p.port(), Phone: "13800000001", Version: "2019"})
	m.mu.Lock()
	m.terms, m.def = []*Terminal{term}, "1"
	m.mu.Unlock()
	defer stopAll(m)
	term.Start()
	p.expect(0x0102, 5*time.Second, nil)
	sc, err := ParseScenario(`name: 断言
steps:
  - {action: expectState, path: conn.status, value: online, within: 5}
  - {action: alarm, bit: 0}
  - {action: expectBit, kind: alarm, bit: 紧急报警, on: true, within: 2}
  - {action: expectDown, msgId: "8104", within: 5}
  - {action: expectDown, msgId: "8999", within: 0.5}
  - {action: expectState, path: loc.speed, op: ">", value: 500}
`)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		p.send(0x8104, nil)
	}()
	if _, err := scenarios.Run(m, sc, "1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scenarios.done("1"):
	case <-time.After(15 * time.Second):
		t.Fatal("场景未结束")
	}
	v := scenarios.Views(m)[0]
	if v.Status != "done" || v.Asserts != 5 || v.AssertFails != 2 || v.Failures != 2 {
		t.Fatalf("%+v", v)
	}
	sum := formatScenarioSummary(v)
	if !strings.Contains(sum, "8999") || !strings.Contains(sum, "结果：失败") || scenarioExit(v) != 1 {
		t.Fatal(sum)
	}
}

// 命令行 -scenario：全部通过退出码 0，断言失败 1，场景无效 2
func TestScenarioCLI(t *testing.T) {
	p := newFakePlat(t, V2019)
	stateDir = t.TempDir()
	if !Quiet {
		Quiet = true
	}
	scenarios = &ScenarioHub{runs: map[string]*scenRun{}}
	dir := t.TempDir()
	write := func(name, text string) string {
		f := filepath.Join(dir, name)
		os.WriteFile(f, []byte(text), 0o644)
		return f
	}
	cfg := Config{Host: "127.0.0.1", Port: p.port(), Version: "2019"}
	pass := write("ok.yaml", `name: 上线检查
config: {phone: "13800000077"}
connect: true
steps:
  - {action: expectState, path: conn.status, value: online, within: 10}
  - {action: report}
`)
	if code := runScenarioCLI(pass, cfg, 30*time.Second); code != 0 {
		t.Fatalf("应通过，退出码 %d", code)
	}
	scenarios = &ScenarioHub{runs: map[string]*scenRun{}}
	fail := write("fail.yaml", "name: 失败\nsteps: [{action: expectState, path: conn.status, value: online, within: 0.3}]\n")
	cfg.Phone = "13800000078"
	if code := runScenarioCLI(fail, cfg, 30*time.Second); code != 1 {
		t.Fatalf("断言失败应返回 1，得到 %d", code)
	}
	if code := runScenarioCLI(write("bad.yaml", "steps: [{action: nope}]"), cfg, 0); code != 2 {
		t.Fatalf("无效场景应返回 2，得到 %d", code)
	}
}
