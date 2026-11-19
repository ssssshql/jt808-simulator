package main

// 场景断言：在脚本里检查“平台是否在 N 秒内下发了某条消息”“某个状态字段是否满足条件”“某个位是否置位”。
// 断言失败计为该步失败（stopOnError: true 时立即终止）；命令行 jt808sim-cli -scenario 按断言结果给出退出码。
//
//   - {after: 0, action: expectDown, msgId: "8103", within: 10}          # 10 秒内收到 8103（默认从上一步开始执行时起算；since: start 从场景开始起算）
//   - {action: expectState, path: conn.status, value: online, within: 30}
//   - {action: expectState, path: loc.speed, op: ">=", value: 50, within: 20}
//   - {action: expectBit, kind: alarm, bit: 超速报警, on: true, within: 15}

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// downRec 最近收到的下行消息（场景断言用）
type downRec struct {
	ID   int
	At   time.Time
	Body []byte
}

const maxDownSeen = 1000

// noteDown 记录收到的下行消息。调用方不持有 t.mu
func (t *Terminal) noteDown(m *Msg) {
	t.mu.Lock()
	t.downSeen = append(t.downSeen, downRec{ID: m.ID, At: time.Now(), Body: m.Body})
	if len(t.downSeen) > maxDownSeen {
		t.downSeen = append([]downRec(nil), t.downSeen[len(t.downSeen)-maxDownSeen:]...)
	}
	t.mu.Unlock()
}

// downSince 自 since 起收到的指定消息条数（bodyPrefix 非空时只算消息体以它开头的）
func (t *Terminal) downSince(id int, since time.Time, bodyPrefix []byte) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, r := range t.downSeen {
		if r.ID == id && !r.At.Before(since) && (len(bodyPrefix) == 0 || strings.HasPrefix(string(r.Body), string(bodyPrefix))) {
			n++
		}
	}
	return n
}

var assertActions = map[string]bool{"expectDown": true, "expectState": true, "expectBit": true}

var bitKinds = map[string]string{"alarm": "alarm", "status": "status", "video": "videoAlarm", "videoAlarm": "videoAlarm", "signal": "signal", "io": "io"}

var stateBitKey = map[string]string{"alarm": "alarms", "status": "status", "videoAlarm": "videoAlarms", "signal": "signals", "io": "io"}

var assertOps = map[string]bool{"==": true, "!=": true, ">": true, ">=": true, "<": true, "<=": true, "contains": true}

// checkAssertStep 解析期校验断言步骤
func checkAssertStep(st ScenarioStep) error {
	p := st.Params
	if w := pf(p, "within", 0); w < 0 || w > 86400 {
		return fmt.Errorf("within 范围 0~86400 秒")
	}
	switch st.Action {
	case "expectDown":
		if _, err := strconv.ParseInt(strings.TrimPrefix(pstr(p, "msgId", ""), "0x"), 16, 32); err != nil {
			return fmt.Errorf("expectDown 需要 msgId（十六进制，如 \"8103\"）")
		}
		if s := pstr(p, "since", "step"); s != "step" && s != "start" {
			return fmt.Errorf("since 只能是 step / start")
		}
		if bp := pstr(p, "bodyPrefix", ""); bp != "" {
			if _, err := parseHexLoose(bp); err != nil {
				return fmt.Errorf("bodyPrefix 需要十六进制")
			}
		}
	case "expectState":
		if strings.TrimSpace(pstr(p, "path", "")) == "" {
			return fmt.Errorf("expectState 需要 path（如 conn.status、loc.speed）")
		}
		if _, ok := p["value"]; !ok {
			return fmt.Errorf("expectState 需要 value")
		}
		if op := pstr(p, "op", "=="); !assertOps[op] {
			return fmt.Errorf("op 只能是 == != > >= < <= contains")
		}
	case "expectBit":
		kind := bitKinds[pstr(p, "kind", "alarm")]
		if kind == "" {
			return fmt.Errorf("kind 只能是 alarm / status / video / signal / io")
		}
		if _, err := stepBit(kind, p["bit"]); err != nil {
			return err
		}
	}
	return nil
}

func parseHexLoose(s string) ([]byte, error) {
	s = strings.NewReplacer(" ", "", "0x", "", "0X", "").Replace(s)
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("长度为奇数")
	}
	b := make([]byte, len(s)/2)
	for i := range b {
		v, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			return nil, err
		}
		b[i] = byte(v)
	}
	return b, nil
}

// statePath 按 a.b.0.c 取 State() 中的值（经 JSON 往返为通用类型）
func statePath(state map[string]any, path string) (any, bool) {
	b, err := json.Marshal(state)
	if err != nil {
		return nil, false
	}
	var cur any
	if json.Unmarshal(b, &cur) != nil {
		return nil, false
	}
	for _, k := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			x, ok := v[k]
			if !ok {
				return nil, false
			}
			cur = x
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func fmtVal(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// compareVal 比较实际值与期望值：两边都能转成数字时按数字比较，否则按字符串
func compareVal(got any, op string, want any) bool {
	if op == "contains" {
		return strings.Contains(fmtVal(got), fmtVal(want))
	}
	a, ok1 := toFloat(got)
	b, ok2 := toFloat(want)
	if ok1 && ok2 {
		switch op {
		case "==":
			return math.Abs(a-b) < 1e-9
		case "!=":
			return math.Abs(a-b) >= 1e-9
		case ">":
			return a > b
		case ">=":
			return a >= b
		case "<":
			return a < b
		case "<=":
			return a <= b
		}
	}
	gs, ws := fmtVal(got), fmtVal(want)
	switch op {
	case "==":
		return gs == ws
	case "!=":
		return gs != ws
	}
	return false
}

// execAssert 执行断言步骤；within 秒内满足即通过
func (r *scenRun) execAssert(st ScenarioStep, since time.Time) error {
	t, p := r.term, st.Params
	within := secs(math.Max(0, pf(p, "within", 0)))
	wait := max(within, time.Nanosecond) // within: 0 = 只检查一次（waitUntil 的 0 表示不限时）
	var cond func() bool
	var desc func() string
	switch st.Action {
	case "expectDown":
		id, _ := strconv.ParseInt(strings.TrimPrefix(pstr(p, "msgId", ""), "0x"), 16, 32)
		if pstr(p, "since", "step") == "start" {
			since = r.started
		}
		prefix, _ := parseHexLoose(pstr(p, "bodyPrefix", ""))
		want := max(1, int(pf(p, "count", 1)))
		got := 0
		cond = func() bool { got = t.downSince(int(id), since, prefix); return got >= want }
		desc = func() string {
			return fmt.Sprintf("期望 %s 秒内收到 %04X ×%d，实际 %d 条", ftoa(within.Seconds(), -1), id, want, got)
		}
	case "expectState":
		path, op, want := pstr(p, "path", ""), pstr(p, "op", "=="), p["value"]
		var got any
		found := false
		cond = func() bool {
			got, found = statePath(t.State(), path)
			return found && compareVal(got, op, want)
		}
		desc = func() string {
			if !found {
				return fmt.Sprintf("状态字段 %s 不存在", path)
			}
			return fmt.Sprintf("期望 %s %s %s，实际 %s", path, op, fmtVal(want), fmtVal(got))
		}
	case "expectBit":
		kind := bitKinds[pstr(p, "kind", "alarm")]
		bit, _ := stepBit(kind, p["bit"])
		wantOn := true
		if _, ok := p["on"]; ok {
			wantOn = pint(p, "on", 1) == 1 || p["on"] == true
		}
		gotOn := false
		cond = func() bool {
			list, _ := t.State()[stateBitKey[kind]].([]Bit)
			gotOn = false
			for _, b := range list {
				if b.Bit == bit {
					gotOn = b.On
				}
			}
			return gotOn == wantOn
		}
		desc = func() string {
			return fmt.Sprintf("期望 %s 位 %d「%s」为 %v，实际 %v", kind, bit, bitActionNames[kind][bit], wantOn, gotOn)
		}
	default:
		return fmt.Errorf("未知断言 %s", st.Action)
	}
	ok, alive := r.waitUntil(wait, cond)
	if !alive {
		return errScenStopped
	}
	r.mu.Lock()
	r.asserts++
	if !ok {
		r.assertFails++
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("断言失败：%s", desc())
	}
	return nil
}
