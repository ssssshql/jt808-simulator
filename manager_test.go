package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func stopAll(m *Manager) {
	m.SaveAll()
	for _, t := range m.List() {
		t.Stop("测试结束")
		select {
		case <-t.quit:
		default:
			close(t.quit)
		}
	}
}

// 旧版本单终端状态（state/<phone>.json，无 terminals.json）自动成为 1 号终端
func TestManagerMigrateLegacyState(t *testing.T) {
	stateDir = t.TempDir()
	old := NewTerminal(Config{Phone: "13912345678", Version: "2013", Host: "10.0.0.1", Port: 9000})
	old.mu.Lock()
	old.Mileage = 777.7
	old.Params[0x0001] = "42"
	old.save()
	old.mu.Unlock()

	m := NewManager(Config{Phone: "13912345678"})
	defer stopAll(m)
	ts := m.List()
	if len(ts) != 1 || ts[0].ID != "1" || m.Default() != "1" {
		t.Fatalf("terms=%d def=%s", len(ts), m.Default())
	}
	t1 := ts[0]
	if t1.Mileage != 777.7 || t1.Params[0x0001] != "42" || t1.Cfg.Version != "2013" || t1.Cfg.Host != "10.0.0.1" {
		t.Fatalf("旧状态未加载: %+v %v", t1.Cfg, t1.Mileage)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "terminals.json")); err != nil {
		t.Fatal("未写终端清单")
	}

	// 新增 / 复制
	t2, err := m.Add(t1, false)
	if err != nil {
		t.Fatal(err)
	}
	if t2.Cfg.Phone != "13912345679" || t2.Cfg.Host != "10.0.0.1" || t2.Cfg.Port != 9000 || t2.Mileage == 777.7 {
		t.Fatalf("add: %+v", t2.Cfg)
	}
	t3, err := m.Add(t1, true)
	if err != nil {
		t.Fatal(err)
	}
	if t3.Cfg.Phone != "13912345680" || t3.Params[0x0001] != "42" || t3.Cfg.Token != "" || t3.Cfg.Plate != "粤A12347" || t3.Cfg.DeviceID != "2345680" {
		t.Fatalf("clone: %+v %v", t3.Cfg, t3.Params[0x0001])
	}
	// 手机号唯一
	if err := t3.Do("setConfig", map[string]any{"phone": "13912345678"}); err == nil {
		t.Fatal("重复手机号应报错")
	}
	if err := t3.Do("setConfig", map[string]any{"phone": "13700000009"}); err != nil {
		t.Fatal(err)
	}
	m.save()
	if err := m.SetDefault(t3.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(t2.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "13912345679.json")); !os.IsNotExist(err) {
		t.Fatal("purge 应删除状态文件")
	}
	stopAll(m)

	// 重启：按清单恢复
	m2 := NewManager(Config{})
	defer stopAll(m2)
	ts = m2.List()
	if len(ts) != 2 || ts[1].Cfg.Phone != "13700000009" || m2.Default() != t3.ID || ts[1].Params[0x0001] != "42" {
		t.Fatalf("reload: n=%d def=%s", len(ts), m2.Default())
	}
	if err := m2.Delete(ts[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if err := m2.Delete(ts[1].ID, false); err == nil {
		t.Fatal("最后一个终端不能删除")
	}
	if incTail("粤A00999", 1) != "粤A01000" || incTail("ABC", 1) != "ABC" || incTail("99", 1) != "00" {
		t.Fatal("incTail")
	}
}

func TestManagerAPI(t *testing.T) {
	stateDir = t.TempDir()
	m := NewManager(Config{Phone: "13800000001"})
	defer stopAll(m)
	h := newWebMux(m)
	call := func(method, path string, body any) map[string]any {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, rd))
		var v map[string]any
		json.Unmarshal(w.Body.Bytes(), &v)
		if v == nil {
			v = map[string]any{"_code": w.Code}
		}
		return v
	}
	r := call("POST", "/api/terminals", map[string]any{"op": "clone", "id": "1"})
	if r["ok"] != true || r["id"] != "2" {
		t.Fatal(r)
	}
	if s := call("GET", "/api/t/2/state", nil); s["id"] != "2" || s["conn"].(map[string]any)["phone"] != "13800000002" {
		t.Fatal(s["id"], s["conn"])
	}
	if s := call("GET", "/api/state", nil); s["id"] != "1" { // 旧接口 = 默认终端
		t.Fatal(s["id"])
	}
	if r := call("POST", "/api/t/2/action", map[string]any{"type": "setMotion", "mode": "drive"}); r["ok"] != true {
		t.Fatal(r)
	}
	if m.Get("2").motion.Mode != "drive" || m.Get("1").motion.Mode != "stop" {
		t.Fatal("动作未按终端隔离")
	}
	if s := call("GET", "/api/t/9/state", nil); s["ok"] != false {
		t.Fatal(s)
	}
	l := call("GET", "/api/terminals", nil)
	if len(l["terminals"].([]any)) != 2 || l["default"] != "1" {
		t.Fatal(l)
	}
	if r := call("POST", "/api/terminals", map[string]any{"op": "delete", "id": "2", "purge": 1}); r["ok"] != true {
		t.Fatal(r)
	}
	if v := call("GET", "/api/version", nil); v["version"] == nil {
		t.Fatal(v)
	}
}
