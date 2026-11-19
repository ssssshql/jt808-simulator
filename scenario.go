package main

// 场景脚本：用 YAML / JSON 描述“哪个终端、走什么轨迹、在什么时间点做什么”，一键运行 / 停止并显示进度。
// 场景库保存在 state/scenarios.json（名称 -> 源文本），与终端状态一起持久化。
//
// 格式（YAML 与 JSON 字段相同）：
//   name: 超速与断线重连
//   terminal: ""            # 终端编号；空 = 页面当前终端；new = 新建终端
//   config: {host: 127.0.0.1, port: 7611}   # 可选：运行前修改终端配置（同 setConfig）
//   connect: true           # 可选：开始时上线并等待在线
//   route:                  # 可选：轨迹（内联点或 GPX/CSV 文本），运行时以轨迹回放模式行驶
//     points: [[113.32, 23.10, 60], [113.33, 23.11]]   # [经度, 纬度, 速度 km/h 可选]
//     # data: |  GPX 或 CSV 原文      format: gpx|csv（缺省自动识别）
//     mult: 1  loop: false
//   steps:                  # 按顺序执行；触发条件 at（开始后第 N 秒）/ after（上一步后 N 秒）/ atPoint（到达轨迹第 N 点）/ atProgress（轨迹进度 0~1）
//     - {at: 5, action: alarm, bit: 1}
//     - {after: 10, action: reconnect, wait: 30}
//     - {atPoint: 20, action: send, msgId: "0900", params: {type: 240, content: HELLO}}
//   duration: 120           # 可选：最后一步之后继续运行到第 N 秒再结束
//   stopOnError: false      # 某步失败时是否终止

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	maxScenarioBytes = 4 << 20
	maxScenarioSteps = 1000
	maxScenarioLib   = 200
)

type ScenarioRoute struct {
	Name   string      `json:"name"`
	Points [][]float64 `json:"points"` // [经度, 纬度, 速度?]
	Data   string      `json:"data"`   // GPX / CSV 原文
	Format string      `json:"format"`
	Mult   float64     `json:"mult"`
	Loop   bool        `json:"loop"`
	Speed  float64     `json:"speed"` // 无点速度时的目标速度
}

type ScenarioStep struct {
	At         *float64       `json:"at,omitempty"`
	After      *float64       `json:"after,omitempty"`
	AtPoint    *int           `json:"atPoint,omitempty"`
	AtProgress *float64       `json:"atProgress,omitempty"`
	Action     string         `json:"action"`
	Note       string         `json:"note,omitempty"`
	Params     map[string]any `json:"params,omitempty"` // 其余字段（bit、on、speed、msgId…）
}

type Scenario struct {
	Name        string         `json:"name"`
	Terminal    string         `json:"terminal"`
	Config      map[string]any `json:"config,omitempty"`
	Connect     bool           `json:"connect"`
	Route       *ScenarioRoute `json:"route,omitempty"`
	Steps       []ScenarioStep `json:"steps"`
	Duration    float64        `json:"duration"`
	StopOnError bool           `json:"stopOnError"`
}

// scenarioActions 场景专用动作（其余动作名直接交给 actions.go 的 Terminal.Do）
var scenarioActions = map[string]string{
	"connect": "上线", "disconnect": "断线", "reconnect": "断线后等待重连", "wait": "等待", "waitOnline": "等待在线",
	"alarm": "报警置位", "clearAlarm": "报警清除", "clearAlarms": "清除全部报警", "status": "状态位", "videoAlarm": "视频报警",
	"signal": "扩展信号位", "io": "IO 状态位", "speed": "改速度", "motion": "运动模式", "position": "设定位置", "report": "立即上报 0200",
	"send": "发送消息", "raw": "发送原始报文", "adas": "主动安全报警", "replay": "回放控制", "log": "写日志", "logout": "注销",
	"expectDown": "断言：收到下行", "expectState": "断言：状态字段", "expectBit": "断言：位状态",
}

// doActions actions.go 中可直接调用的动作
var doActions = map[string]bool{"setConfig": true, "connect": true, "disconnect": true, "logout": true, "setMotion": true, "setPosition": true,
	"setVehicle": true, "report": true, "setStatus": true, "setAlarm": true, "setVideoAlarm": true, "clearAlarms": true, "send": true, "adas": true,
	"gdPeriph": true, "gdReport": true, "gdSet": true, "setParam": true, "setParams": true, "setSignal": true, "setIO": true, "setAnalog": true,
	"setDriver": true, "setReply": true, "raw": true, "setReplay": true, "photo": true}

// ParseScenario 解析 YAML / JSON 场景并校验
func ParseScenario(text string) (*Scenario, error) {
	if len(text) > maxScenarioBytes {
		return nil, fmt.Errorf("场景过大（最大 %d MB）", maxScenarioBytes>>20)
	}
	text = strings.TrimPrefix(text, "\ufeff")
	var raw map[string]any
	if strings.HasPrefix(strings.TrimSpace(text), "{") {
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			return nil, fmt.Errorf("JSON 解析失败: %v", err)
		}
	} else if err := yaml.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %v", err)
	}
	if raw == nil {
		return nil, fmt.Errorf("场景为空")
	}
	// 经 JSON 往返统一数值类型（YAML 的 int → float64，与页面动作参数一致）
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("场景内容无效: %v", err)
	}
	var doc struct {
		Scenario
		Steps []map[string]any `json:"steps"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("场景字段类型错误: %v", err)
	}
	sc := doc.Scenario
	sc.Name = strings.TrimSpace(sc.Name)
	if sc.Name == "" {
		sc.Name = "未命名场景"
	}
	if len(doc.Steps) > maxScenarioSteps {
		return nil, fmt.Errorf("步骤过多（最多 %d）", maxScenarioSteps)
	}
	sc.Steps = nil
	for i, m := range doc.Steps {
		st, err := parseStep(m)
		if err != nil {
			return nil, fmt.Errorf("第 %d 步: %v", i+1, err)
		}
		sc.Steps = append(sc.Steps, st)
	}
	if r := sc.Route; r != nil {
		if len(r.Points) == 0 && strings.TrimSpace(r.Data) == "" {
			return nil, fmt.Errorf("route 需要 points 或 data")
		}
		if len(r.Points) > 0 && len(r.Points) < 2 {
			return nil, fmt.Errorf("route.points 至少 2 个点")
		}
		for i, p := range r.Points {
			if len(p) < 2 || !validLL(p[1], p[0]) {
				return nil, fmt.Errorf("route.points 第 %d 点无效（格式 [经度, 纬度, 速度]）", i+1)
			}
		}
		if r.Mult != 0 && (r.Mult < 0.1 || r.Mult > 100) {
			return nil, fmt.Errorf("route.mult 范围 0.1~100")
		}
		if _, _, err := ParseRoute(r.routeData(), r.Format); err != nil {
			return nil, fmt.Errorf("route: %v", err)
		}
	}
	if sc.Duration < 0 || sc.Duration > 7*86400 {
		return nil, fmt.Errorf("duration 范围 0~604800 秒")
	}
	if len(sc.Steps) == 0 && sc.Route == nil && !sc.Connect {
		return nil, fmt.Errorf("场景没有任何步骤（steps）")
	}
	return &sc, nil
}

func parseStep(m map[string]any) (ScenarioStep, error) {
	st := ScenarioStep{Params: map[string]any{}}
	num := func(k string) (*float64, error) {
		v, ok := m[k]
		if !ok {
			return nil, nil
		}
		f, ok := v.(float64)
		if !ok || f < 0 || math.IsNaN(f) || f > 7*86400 {
			return nil, fmt.Errorf("%s 需要非负数字（秒）", k)
		}
		return &f, nil
	}
	var err error
	if st.At, err = num("at"); err != nil {
		return st, err
	}
	if st.After, err = num("after"); err != nil {
		return st, err
	}
	if p, err := num("atPoint"); err != nil {
		return st, err
	} else if p != nil {
		n := int(*p)
		st.AtPoint = &n
	}
	if st.AtProgress, err = num("atProgress"); err != nil {
		return st, err
	}
	if st.AtProgress != nil && *st.AtProgress > 1 {
		return st, fmt.Errorf("atProgress 范围 0~1")
	}
	n := 0
	for _, x := range []bool{st.At != nil, st.After != nil, st.AtPoint != nil, st.AtProgress != nil} {
		if x {
			n++
		}
	}
	if n > 1 {
		return st, fmt.Errorf("at / after / atPoint / atProgress 只能选一个")
	}
	st.Action, _ = m["action"].(string)
	st.Note, _ = m["note"].(string)
	if st.Action == "" {
		return st, fmt.Errorf("缺少 action")
	}
	if scenarioActions[st.Action] == "" && !doActions[st.Action] {
		return st, fmt.Errorf("未知动作 %q", st.Action)
	}
	for k, v := range m {
		switch k {
		case "at", "after", "atPoint", "atProgress", "action", "note":
		default:
			st.Params[k] = v
		}
	}
	switch st.Action {
	case "alarm", "clearAlarm", "status", "videoAlarm", "signal", "io":
		if _, err := stepBit(st.Action, st.Params["bit"]); err != nil {
			return st, err
		}
	case "send":
		if _, err := strconv.ParseInt(strings.TrimPrefix(pstr(st.Params, "msgId", ""), "0x"), 16, 32); err != nil {
			return st, fmt.Errorf("send 需要 msgId（十六进制，如 \"0900\"）")
		}
	case "expectDown", "expectState", "expectBit":
		if err := checkAssertStep(st); err != nil {
			return st, err
		}
	}
	return st, nil
}

// routeData 内联点转成 CSV（lng,lat[,speed]）
func (r *ScenarioRoute) routeData() []byte {
	if len(r.Points) == 0 {
		return []byte(r.Data)
	}
	var b strings.Builder
	for _, p := range r.Points {
		b.WriteString(ftoa(p[0], 7) + "," + ftoa(p[1], 7))
		if len(p) > 2 {
			b.WriteString("," + ftoa(p[2], 2))
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

var bitActionNames = map[string]map[int]string{"alarm": alarmNames, "clearAlarm": alarmNames, "status": statusNames,
	"videoAlarm": videoAlarmNames, "signal": extSignalNames, "io": ioNames}

// stepBit 位可写数字或名称（如 "超速报警"）
func stepBit(action string, v any) (int, error) {
	names := bitActionNames[action]
	switch x := v.(type) {
	case float64:
		if names[int(x)] != "" {
			return int(x), nil
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil && names[n] != "" {
			return n, nil
		}
		for b, n := range names {
			if n == strings.TrimSpace(x) {
				return b, nil
			}
		}
	}
	return 0, fmt.Errorf("%s 的 bit 无效：%v", action, v)
}

func (st ScenarioStep) label() string {
	when := "立即"
	switch {
	case st.At != nil:
		when = "t=" + ftoa(*st.At, -1) + "s"
	case st.After != nil:
		when = "+" + ftoa(*st.After, -1) + "s"
	case st.AtPoint != nil:
		when = "轨迹点 " + itoa(*st.AtPoint)
	case st.AtProgress != nil:
		when = "进度 " + ftoa(*st.AtProgress*100, -1) + "%"
	}
	name := scenarioActions[st.Action]
	if name == "" {
		name = st.Action
	}
	var kv []string
	for k, v := range st.Params {
		b, _ := json.Marshal(v)
		kv = append(kv, k+"="+string(b))
	}
	sort.Strings(kv)
	s := when + " " + name
	if len(kv) > 0 {
		s += " " + strings.Join(kv, " ")
	}
	if st.Note != "" {
		s += " · " + st.Note
	}
	return s
}

// ---------------- 运行 ----------------

type scenLog struct {
	T     float64 `json:"t"` // 开始后秒数
	Step  int     `json:"step"`
	OK    bool    `json:"ok"`
	Text  string  `json:"text"`
	Error string  `json:"error,omitempty"`
}

type scenRun struct {
	mu          sync.Mutex
	sc          *Scenario
	term        *Terminal
	status      string // running / done / stopped / error
	step        int    // 正在等待/执行的步骤下标
	started     time.Time
	ended       time.Time
	err         string
	log         []scenLog
	stop        chan struct{}
	done        chan struct{}
	failures    int
	last        time.Time // 上一步开始执行的时间（断言 expectDown 默认从这里起算）
	asserts     int       // 已执行的断言数
	assertFails int       // 失败的断言数
}

type ScenRunView struct {
	Terminal    string    `json:"terminal"`
	Phone       string    `json:"phone"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Step        int       `json:"step"`
	Total       int       `json:"total"`
	Steps       []string  `json:"steps"`
	Elapsed     float64   `json:"elapsed"`
	Started     int64     `json:"started"`
	Error       string    `json:"error,omitempty"`
	Failures    int       `json:"failures"`
	Asserts     int       `json:"asserts"`
	AssertFails int       `json:"assertFails"`
	Log         []scenLog `json:"log"`
	Progress    float64   `json:"progress"`
}

func (r *scenRun) view() ScenRunView {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := time.Now()
	if !r.ended.IsZero() {
		end = r.ended
	}
	v := ScenRunView{Terminal: r.term.ID, Phone: r.term.phoneNow(), Name: r.sc.Name, Status: r.status, Step: r.step, Total: len(r.sc.Steps),
		Elapsed: math.Round(end.Sub(r.started).Seconds()*10) / 10, Started: r.started.UnixMilli(), Error: r.err, Failures: r.failures, Asserts: r.asserts, AssertFails: r.assertFails,
		Log: append([]scenLog{}, r.log...)}
	for _, st := range r.sc.Steps {
		v.Steps = append(v.Steps, st.label())
	}
	if v.Total > 0 {
		v.Progress = float64(min(r.step, v.Total)) / float64(v.Total)
	}
	if r.status == "done" {
		v.Progress = 1
	}
	return v
}

func (r *scenRun) addLog(step int, ok bool, text, err string) {
	r.mu.Lock()
	r.log = append(r.log, scenLog{T: math.Round(time.Since(r.started).Seconds()*10) / 10, Step: step, OK: ok, Text: text, Error: err})
	if len(r.log) > 200 {
		r.log = r.log[len(r.log)-200:]
	}
	if !ok {
		r.failures++
	}
	r.mu.Unlock()
}

// sleep 可中断等待；被停止返回 false
func (r *scenRun) sleep(d time.Duration) bool {
	if d <= 0 {
		select {
		case <-r.stop:
			return false
		default:
			return true
		}
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-r.stop:
		return false
	case <-tm.C:
		return true
	}
}

// waitUntil 轮询条件直到满足（或超时 / 被停止）
func (r *scenRun) waitUntil(timeout time.Duration, cond func() bool) (bool, bool) {
	end := time.Now().Add(timeout)
	for {
		if cond() {
			return true, true
		}
		if timeout > 0 && time.Now().After(end) {
			return false, true
		}
		if !r.sleep(100 * time.Millisecond) {
			return false, false
		}
	}
}

// ScenarioHub 每个终端同时最多运行一个场景
type ScenarioHub struct {
	mu   sync.Mutex
	runs map[string]*scenRun // 终端编号 -> 最近一次运行
	lib  []ScenarioDoc
	ok   bool // 已从磁盘加载
}

type ScenarioDoc struct {
	Name    string `json:"name"`
	Text    string `json:"text"`
	Updated int64  `json:"updated"`
}

var scenarios = &ScenarioHub{runs: map[string]*scenRun{}}

// Run 在终端上运行场景；terminal 为空时用 cur（页面当前终端），"new" 新建终端
func (h *ScenarioHub) Run(m *Manager, sc *Scenario, cur string) (*Terminal, error) {
	id := strings.TrimSpace(sc.Terminal)
	if id == "" {
		id = cur
	}
	var t *Terminal
	if id == "new" {
		var err error
		if t, err = m.Add(m.Get(cur), false); err != nil {
			return nil, err
		}
	} else if t = m.Get(id); t == nil {
		return nil, fmt.Errorf("终端 #%s 不存在", id)
	}
	h.mu.Lock()
	if old := h.runs[t.ID]; old != nil {
		old.mu.Lock()
		busy := old.status == "running"
		old.mu.Unlock()
		if busy {
			h.mu.Unlock()
			return nil, fmt.Errorf("终端 #%s 已有场景「%s」在运行，请先停止", t.ID, old.sc.Name)
		}
	}
	r := &scenRun{sc: sc, term: t, status: "running", started: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	h.runs[t.ID] = r
	h.mu.Unlock()
	go r.run(m)
	return t, nil
}

// done 终端最近一次运行的结束通道（没有运行时返回已关闭的通道）
func (h *ScenarioHub) done(id string) <-chan struct{} {
	h.mu.Lock()
	r := h.runs[id]
	h.mu.Unlock()
	if r == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	return r.done
}

// Stop 停止终端上正在运行的场景
func (h *ScenarioHub) Stop(id string) bool {
	h.mu.Lock()
	r := h.runs[id]
	h.mu.Unlock()
	if r == nil {
		return false
	}
	r.mu.Lock()
	running := r.status == "running"
	if running {
		r.status = "stopped"
		close(r.stop)
	}
	r.mu.Unlock()
	if running {
		<-r.done
	}
	return running
}

func (h *ScenarioHub) Views(m *Manager) []ScenRunView {
	h.mu.Lock()
	var rs []*scenRun
	for id, r := range h.runs {
		if m != nil && m.Get(id) != r.term { // 终端已删除
			delete(h.runs, id)
			continue
		}
		rs = append(rs, r)
	}
	h.mu.Unlock()
	out := make([]ScenRunView, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.view())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started > out[j].Started })
	return out
}

func (r *scenRun) finish(status, err string) {
	r.mu.Lock()
	already := !r.ended.IsZero()
	if !already {
		if r.status == "running" {
			r.status = status
		}
		r.err = err
		r.ended = time.Now()
	}
	r.mu.Unlock()
	if already {
		return
	}
	close(r.done)
	t := r.term
	t.logf("场景「%s」结束：%s%s", r.sc.Name, map[string]string{"done": "完成", "stopped": "已停止", "error": "出错"}[r.view().Status],
		map[bool]string{true: "（" + err + "）", false: ""}[err != ""])
}

func (r *scenRun) run(m *Manager) {
	defer func() {
		if e := recover(); e != nil {
			r.finish("error", fmt.Sprintf("内部异常: %v", e))
		}
	}()
	t, sc := r.term, r.sc
	t.logf("场景「%s」开始：%d 步", sc.Name, len(sc.Steps))
	if len(sc.Config) > 0 {
		if err := t.Do("setConfig", sc.Config); err != nil {
			r.addLog(-1, false, "修改终端配置", err.Error())
			r.finish("error", "修改终端配置失败: "+err.Error())
			return
		}
		m.save()
		r.addLog(-1, true, "修改终端配置", "")
	}
	if rt := sc.Route; rt != nil {
		name := firstNonEmpty(rt.Name, sc.Name)
		if _, err := t.importRoute(name, rt.Format, rt.routeData()); err != nil {
			r.finish("error", "导入轨迹失败: "+err.Error())
			return
		}
		p := map[string]any{"loop": map[bool]float64{true: 1, false: 0}[rt.Loop], "restart": float64(1)}
		if rt.Mult > 0 {
			p["mult"] = rt.Mult
		}
		t.replayAction("setReplay", p)
		mp := map[string]any{"mode": "track"}
		if rt.Speed > 0 {
			mp["speed"] = rt.Speed
		}
		if err := t.Do("setMotion", mp); err != nil {
			r.finish("error", err.Error())
			return
		}
		r.addLog(-1, true, "导入轨迹并开始回放", "")
	}
	if sc.Connect {
		if err := r.exec(ScenarioStep{Action: "connect", Params: map[string]any{}}); err != nil {
			r.finish("error", err.Error())
			return
		}
		if err := r.exec(ScenarioStep{Action: "waitOnline", Params: map[string]any{"timeout": float64(60)}}); err != nil {
			if err == errScenStopped {
				r.finish("stopped", "")
				return
			}
			r.addLog(-1, false, "等待在线", err.Error())
		}
	}
	last := time.Now()
	prevStart := r.started // 上一步开始执行的时间（expectDown 默认从这里起算，包含上一步触发的平台下发）
	for i, st := range sc.Steps {
		r.mu.Lock()
		r.step = i
		r.mu.Unlock()
		ok, alive := r.trigger(st, last)
		if !alive {
			r.finish("stopped", "")
			return
		}
		if !ok {
			r.addLog(i, false, st.label(), "触发条件未满足（轨迹已结束）")
			if sc.StopOnError {
				r.finish("error", fmt.Sprintf("第 %d 步触发条件未满足", i+1))
				return
			}
			continue
		}
		r.mu.Lock()
		r.last = prevStart
		r.mu.Unlock()
		prevStart = time.Now()
		err := r.exec(st)
		last = time.Now()
		if err == errScenStopped {
			r.finish("stopped", "")
			return
		}
		r.addLog(i, err == nil, st.label(), errStr(err))
		if err != nil && sc.StopOnError {
			r.finish("error", fmt.Sprintf("第 %d 步失败: %v", i+1, err))
			return
		}
	}
	r.mu.Lock()
	r.step = len(sc.Steps)
	r.mu.Unlock()
	if sc.Duration > 0 {
		if !r.sleep(time.Until(r.started.Add(secs(sc.Duration)))) {
			r.finish("stopped", "")
			return
		}
	}
	r.finish("done", "")
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

var errScenStopped = fmt.Errorf("场景已停止")

// trigger 等待步骤的触发条件。返回 (满足, 未被停止)
func (r *scenRun) trigger(st ScenarioStep, last time.Time) (bool, bool) {
	t := r.term
	switch {
	case st.At != nil:
		return true, r.sleep(time.Until(r.started.Add(secs(*st.At))))
	case st.After != nil:
		return true, r.sleep(time.Until(last.Add(secs(*st.After))))
	case st.AtPoint != nil || st.AtProgress != nil:
		return r.waitUntil(0, func() bool {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.Replay == nil || len(t.Replay.Points) < 2 {
				return true // 没有轨迹：立即执行
			}
			if !t.rp.ready {
				t.prepareReplay()
			}
			if st.AtPoint != nil && (t.rp.seg+1 >= *st.AtPoint || t.rp.done) {
				return true
			}
			if st.AtProgress != nil {
				p, _ := t.replayView()["progress"].(float64)
				return p >= *st.AtProgress || t.rp.done
			}
			return false
		})
	}
	return true, true
}

// exec 执行一个动作
func (r *scenRun) exec(st ScenarioStep) error {
	t := r.term
	p := map[string]any{}
	for k, v := range st.Params {
		p[k] = v
	}
	bit := func(on bool) error {
		b, err := stepBit(st.Action, p["bit"])
		if err != nil {
			return err
		}
		if _, ok := p["on"]; ok && st.Action != "clearAlarm" {
			on = pint(p, "on", 1) == 1
		}
		typ := map[string]string{"alarm": "setAlarm", "clearAlarm": "setAlarm", "status": "setStatus", "videoAlarm": "setVideoAlarm",
			"signal": "setSignal", "io": "setIO"}[st.Action]
		return t.setBit(typ, b, on)
	}
	switch st.Action {
	case "wait":
		if !r.sleep(secs(math.Max(0, pf(p, "seconds", 0)))) {
			return errScenStopped
		}
	case "connect":
		return t.Do("connect", nil)
	case "disconnect":
		t.Stop("场景脚本断线")
	case "reconnect":
		t.Stop("场景脚本断线")
		if !r.sleep(secs(math.Max(0, pf(p, "wait", 5)))) {
			return errScenStopped
		}
		return t.Do("connect", nil)
	case "waitOnline":
		ok, alive := r.waitUntil(secs(math.Max(1, pf(p, "timeout", 60))), func() bool {
			t.mu.Lock()
			defer t.mu.Unlock()
			return t.status == "online"
		})
		if !alive {
			return errScenStopped
		}
		if !ok {
			return fmt.Errorf("等待在线超时")
		}
	case "alarm", "status", "videoAlarm", "signal", "io":
		return bit(true)
	case "clearAlarm":
		return bit(false)
	case "speed":
		return t.Do("setMotion", map[string]any{"speed": pf(p, "speed", pf(p, "value", 60))})
	case "motion":
		return t.Do("setMotion", p)
	case "position":
		return t.Do("setPosition", p)
	case "replay":
		return t.Do("setReplay", p)
	case "log":
		t.logf("场景：%s", firstNonEmpty(pstr(p, "text", ""), st.Note))
	case "expectDown", "expectState", "expectBit":
		r.mu.Lock()
		since := r.last
		r.mu.Unlock()
		if since.IsZero() {
			since = r.started
		}
		return r.execAssert(st, since)
	default:
		return t.Do(st.Action, p)
	}
	return nil
}

func pf(p map[string]any, k string, def float64) float64 {
	switch v := p[k].(type) {
	case float64:
		return v
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return def
}

// ---------------- 场景库 ----------------

func scenarioFile() string { return filepath.Join(stateDir, "scenarios.json") }

func (h *ScenarioHub) loadLib() {
	if h.ok {
		return
	}
	h.ok = true
	h.lib = nil
	if _, err := readJSONDurable(scenarioFile(), &h.lib); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errf("读取 %s 失败: %v", scenarioFile(), err)
	}
}

func (h *ScenarioHub) saveLib() error {
	b, _ := json.MarshalIndent(h.lib, "", " ")
	return writeFileDurable(scenarioFile(), b)
}

func (h *ScenarioHub) Library() []ScenarioDoc {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loadLib()
	return append([]ScenarioDoc{}, h.lib...)
}

// Save 按场景名保存（同名覆盖）；保存前校验
func (h *ScenarioHub) Save(text string) (string, error) {
	sc, err := ParseScenario(text)
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loadLib()
	doc := ScenarioDoc{Name: sc.Name, Text: text, Updated: time.Now().UnixMilli()}
	for i := range h.lib {
		if h.lib[i].Name == sc.Name {
			h.lib[i] = doc
			return sc.Name, h.saveLib()
		}
	}
	if len(h.lib) >= maxScenarioLib {
		return "", fmt.Errorf("场景库最多 %d 个", maxScenarioLib)
	}
	h.lib = append(h.lib, doc)
	return sc.Name, h.saveLib()
}

func (h *ScenarioHub) Delete(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loadLib()
	for i := range h.lib {
		if h.lib[i].Name == name {
			h.lib = append(h.lib[:i], h.lib[i+1:]...)
			return h.saveLib()
		}
	}
	return fmt.Errorf("场景「%s」不存在", name)
}

const scenarioExample = `# 场景示例：沿轨迹行驶，途中超速、断线 30 秒后重连、紧急报警并发送透传
name: 示例-超速与断线重连
terminal: ""        # 空 = 页面当前终端；new = 新建终端；或终端编号如 "2"
connect: true       # 开始时上线并等待在线
route:
  mult: 5           # 回放倍速
  points:           # [经度, 纬度, 速度 km/h]；也可用 data: | 粘贴 GPX/CSV 原文
    - [113.3245, 23.1065, 50]
    - [113.3300, 23.1080, 60]
    - [113.3360, 23.1110, 90]
    - [113.3420, 23.1150, 60]
    - [113.3480, 23.1190, 40]
steps:
  - {at: 3, action: log, text: 场景开始}
  - {atPoint: 2, action: alarm, bit: 超速报警}
  - {atPoint: 4, action: clearAlarm, bit: 1}
  - {after: 2, action: reconnect, wait: 30}
  - {after: 0, action: waitOnline, timeout: 60}
  - {after: 5, action: alarm, bit: 0}
  - {after: 5, action: send, msgId: "0900", params: {type: 240, content: HELLO}}
  - {after: 10, action: clearAlarms}
  - {after: 0, action: speed, speed: 0}
`

// registerScenarioAPI GET /api/scenarios 场景库 + 运行状态；POST /api/scenarios {op: validate|save|delete|run|stop}
func registerScenarioAPI(mux *http.ServeMux, m *Manager) {
	mux.HandleFunc("GET /api/scenarios", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{"library": scenarios.Library(), "runs": scenarios.Views(m), "example": scenarioExample})
	})
	mux.HandleFunc("POST /api/scenarios", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Op       string `json:"op"`
			Text     string `json:"text"`
			Name     string `json:"name"`
			Terminal string `json:"terminal"` // 页面当前终端（场景未指定终端时使用）
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxScenarioBytes+4096)).Decode(&p); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "JSON 无效: " + err.Error()})
			return
		}
		res := map[string]any{"ok": true}
		var err error
		switch p.Op {
		case "validate":
			var sc *Scenario
			if sc, err = ParseScenario(p.Text); err == nil {
				var steps []string
				for _, st := range sc.Steps {
					steps = append(steps, st.label())
				}
				res["name"], res["steps"] = sc.Name, steps
			}
		case "save":
			var name string
			if name, err = scenarios.Save(p.Text); err == nil {
				res["name"] = name
			}
		case "delete":
			err = scenarios.Delete(p.Name)
		case "run":
			var sc *Scenario
			if sc, err = ParseScenario(p.Text); err == nil {
				var t *Terminal
				if t, err = scenarios.Run(m, sc, p.Terminal); err == nil {
					res["terminal"] = t.ID
				}
			}
		case "stop":
			if !scenarios.Stop(p.Terminal) {
				err = fmt.Errorf("终端 #%s 没有正在运行的场景", p.Terminal)
			}
		default:
			err = fmt.Errorf("未知操作 %s（validate/save/delete/run/stop）", p.Op)
		}
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, res)
	})
}
