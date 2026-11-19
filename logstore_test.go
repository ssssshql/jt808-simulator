package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resetLogDiskLocked() {
	logDisk.active = false
	logDisk.set = LogSettings{}
	logDisk.loaded = false
	logDisk.closeLocked()
	logDisk.day = ""
	logDisk.part = 0
	logDisk.size = 0
	logDisk.lastErr = ""
	logDisk.dropped.Store(0)
}

// 测试用：独立的落盘日志实例（指向临时状态目录）
func freshLogDisk(t *testing.T, enabled bool) {
	t.Helper()
	logDisk.Flush()
	logDisk.mu.Lock()
	resetLogDiskLocked()
	logDisk.mu.Unlock()
	if err := logDisk.SetSettings(LogSettings{Enabled: enabled, MaxMB: 10, Days: 3}); err != nil {
		t.Fatal(err)
	}
	if enabled {
		logDisk.Start()
	}
	t.Cleanup(func() {
		logDisk.Flush()
		logDisk.mu.Lock()
		resetLogDiskLocked()
		logDisk.mu.Unlock()
	})
}

func readCSV(t *testing.T, b []byte) [][]string {
	t.Helper()
	s := strings.TrimPrefix(string(b), "\ufeff")
	rows, err := csv.NewReader(strings.NewReader(s)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestLogExportMemory(t *testing.T) {
	stateDir = t.TempDir()
	freshLogDisk(t, false)
	m := NewManager(Config{Phone: "13800000001"})
	defer stopAll(m)
	t2, _ := m.Add(m.Get("1"), false)
	t1 := m.Get("1")
	t1.logMsg("up", 0x0200, 7, 28, []byte{0x7e, 0x02, 0x00, 0x7e}, "位置 A")
	t1.logMsg("down", 0x8103, 9, 5, []byte{0x7e, 0x81, 0x03, 0x7e}, "设置参数")
	t2.logMsg("up", 0x0002, 1, 0, []byte{0x7e, 0x00, 0x02, 0x7e}, "心跳")

	f, err := parseLogFilter(url.Values{"term": {"1"}, "dir": {"up,down"}})
	if err != nil {
		t.Fatal(err)
	}
	data, n, src, err := exportLogs(m, f, "csv", "")
	if err != nil || src != "memory" || n != 2 || !strings.HasPrefix(string(data), "\ufeff") {
		t.Fatalf("n=%d src=%s err=%v", n, src, err)
	}
	rows := readCSV(t, data)
	if len(rows) != 3 || rows[0][0] != "时间" || rows[1][1] != "#1" || rows[1][2] != "13800000001" || rows[1][3] != "上行" ||
		rows[1][4] != "0200" || rows[1][5] != "位置信息汇报" || rows[1][6] != "7" || rows[1][7] != "28" || rows[1][9] != "7e02007e" || rows[2][4] != "8103" {
		t.Fatalf("%q", rows)
	}
	// 消息 ID 过滤（全部终端，带 0x 前缀）
	f, _ = parseLogFilter(url.Values{"msgId": {"0x0002"}})
	data, n, _, _ = exportLogs(m, f, "csv", "memory")
	if rows := readCSV(t, data); n != 1 || rows[1][1] != "#2" {
		t.Fatalf("%q", rows)
	}
	// 原始 hex：只有上下行帧
	f, _ = parseLogFilter(url.Values{})
	data, n, _, _ = exportLogs(m, f, "hex", "memory")
	lines := 0
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			lines++
		}
	}
	if n != 3 || lines != 3 || !strings.Contains(string(data), "\n7e81037e\n") {
		t.Fatalf("n=%d\n%s", n, data)
	}
	// 时间范围：未来时间段为空
	f, _ = parseLogFilter(url.Values{"from": {fmt.Sprint(time.Now().Add(time.Hour).UnixMilli())}})
	if _, n, _, _ := exportLogs(m, f, "csv", "memory"); n != 0 {
		t.Fatalf("n=%d", n)
	}
	for _, q := range []url.Values{{"dir": {"left"}}, {"msgId": {"zz"}}, {"from": {"昨天"}}, {"from": {"2026-01-02"}, "to": {"2026-01-01"}}} {
		if _, err := parseLogFilter(q); err == nil {
			t.Errorf("%v 应报错", q)
		}
	}
}

func TestLogDiskAndAPI(t *testing.T) {
	stateDir = t.TempDir()
	freshLogDisk(t, true)
	m := NewManager(Config{Phone: "13800000001"})
	defer stopAll(m)
	t1 := m.Get("1")
	for i := 0; i < 2500; i++ { // 超过内存环 2000 条，落盘应全部保留
		t1.logMsg("up", 0x0200, i, 28, []byte{0x7e, 0x02, 0x00, 0x7e}, fmt.Sprintf("位置 %d", i))
	}
	logDisk.Flush()
	files := listLogFiles()
	if len(files) != 1 || files[0].Day != time.Now().Format("2006-01-02") {
		t.Fatalf("%+v", files)
	}
	srv := httptest.NewServer(newWebMux(m))
	defer srv.Close()
	r, err := srv.Client().Get(srv.URL + "/api/logs/export?term=1&msgId=0200")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.Header.Get("X-JT808-Source") != "disk" || r.Header.Get("X-JT808-Rows") != "2500" || !strings.Contains(r.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("%v", r.Header)
	}
	if rows := readCSV(t, b); len(rows) != 2501 || rows[2500][8] != "位置 2499" {
		t.Fatalf("rows=%d", len(rows))
	}
	// 非桌面版 save=1：返回错误 JSON
	r, _ = srv.Client().Get(srv.URL + "/api/logs/export?save=1")
	b, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "只有桌面版") {
		t.Fatal(string(b))
	}
	// 桌面版保存钩子
	var saved string
	saveFileHook = func(name string, data []byte) (string, error) { saved = name; return "/tmp/" + name, nil }
	defer func() { saveFileHook = nil }()
	r, _ = srv.Client().Get(srv.URL + "/api/logs/export?format=hex&term=1&save=1")
	b, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), `"ok":true`) || !strings.HasPrefix(saved, "jt808-log-t1-") || !strings.HasSuffix(saved, ".txt") {
		t.Fatal(string(b), saved)
	}
	// 设置
	r, _ = srv.Client().Post(srv.URL+"/api/logs/settings", "application/json", strings.NewReader(`{"enabled":true,"maxMB":5,"days":3}`))
	b, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "10~100000") {
		t.Fatal(string(b))
	}
}

// 超过保留天数 / 总大小的旧文件被清理
func TestLogPrune(t *testing.T) {
	stateDir = t.TempDir()
	freshLogDisk(t, false)
	os.MkdirAll(logDir(), 0o755)
	old := time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	y := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	big := make([]byte, 6<<20)
	os.WriteFile(filepath.Join(logDir(), old+".jsonl"), []byte("{}\n"), 0o644)
	os.WriteFile(filepath.Join(logDir(), y+".jsonl"), big, 0o644)
	os.WriteFile(filepath.Join(logDir(), y+"-1.jsonl"), big, 0o644)
	os.WriteFile(filepath.Join(logDir(), "notes.txt"), []byte("x"), 0o644)
	logDisk.prune() // 上限 10 MB，保留 3 天
	var names []string
	for _, f := range listLogFiles() {
		names = append(names, f.Name)
	}
	if len(names) != 1 || names[0] != y+"-1.jsonl" {
		t.Fatalf("%v", names)
	}
	if partNo(y+"-12.jsonl") != 12 || partNo(y+".jsonl") != 0 {
		t.Fatal("partNo")
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{"a/b/c.csv": "c.csv", `..\..\x.txt`: "x.txt", "a:b?.csv": "a_b_.csv", "": "download", "..": "download"} {
		if got := safeName(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
