package main

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http/httptest"
	"testing"
)

const testGPX = `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" creator="t" xmlns="http://www.topografix.com/GPX/1/1" xmlns:gpxtpx="http://www.garmin.com/xmlschemas/TrackPointExtension/v2">
<trk><name>测试</name><trkseg>
<trkpt lat="23.100000" lon="113.300000"><ele>10</ele><time>2026-10-31T08:00:00Z</time><extensions><gpxtpx:TrackPointExtension><gpxtpx:speed>10</gpxtpx:speed><gpxtpx:course>90</gpxtpx:course></gpxtpx:TrackPointExtension></extensions></trkpt>
<trkpt lat="23.100000" lon="113.301000"><ele>20</ele><time>2026-10-31T08:00:10Z</time></trkpt>
<trkpt lat="23.101000" lon="113.301000"><ele>30</ele><time>2026-10-31T08:00:20Z</time></trkpt>
</trkseg></trk>
<wpt lat="1" lon="1"><name>忽略</name></wpt>
</gpx>`

func TestParseRoute(t *testing.T) {
	pts, f, err := ParseRoute([]byte(testGPX), "")
	if err != nil || f != "gpx" || len(pts) != 3 {
		t.Fatalf("gpx: %v %s %d", err, f, len(pts))
	}
	if pts[0].Speed == nil || math.Abs(*pts[0].Speed-36) > 1e-9 || *pts[0].Dir != 90 || *pts[1].Alt != 20 || pts[2].T-pts[0].T != 20000 {
		t.Fatalf("gpx 字段: %+v", pts[0])
	}
	csv := "113.30,23.10,30,2026-10-31 08:00:00,5,0\n113.301,23.10,40,2026-10-31 08:00:10,6,90\nbad,line\n113.301,23.101\n"
	pts, f, err = ParseRoute([]byte(csv), "")
	if err != nil || f != "csv" || len(pts) != 3 || *pts[1].Speed != 40 || pts[1].T-pts[0].T != 10000 || pts[2].Speed != nil {
		t.Fatalf("csv: %v %d", err, len(pts))
	}
	hdr := "\xef\xbb\xbftime;latitude;longitude;speed\n1730361600;23.1;113.3;12\n1730361601;23.1001;113.3;13\n"
	pts, _, err = ParseRoute([]byte(hdr), "")
	if err != nil || len(pts) != 2 || pts[0].Lat != 23.1 || pts[0].Lng != 113.3 || *pts[1].Speed != 13 || pts[1].T-pts[0].T != 1000 {
		t.Fatalf("csv 表头: %v %+v", err, pts)
	}
	for _, bad := range []string{"", "113.3,23.1\n", "<gpx></gpx>", "<gpx><trkpt lat='x' lon='1'/></gpx>"} {
		if _, _, err := ParseRoute([]byte(bad), ""); err == nil {
			t.Fatalf("应报错: %q", bad)
		}
	}
	if ms, ok := parseTime("251031080000"); !ok || ms == 0 {
		t.Fatal("BCD 时间")
	}
}

func TestReplayPlayback(t *testing.T) {
	stateDir = t.TempDir()
	term := NewTerminal(Config{Phone: "13800000088"})
	if err := term.Do("setMotion", map[string]any{"mode": "track"}); err == nil {
		t.Fatal("未导入轨迹时不能切换到 track")
	}
	if _, err := term.importRoute("g", "", []byte(testGPX)); err != nil {
		t.Fatal(err)
	}
	if err := term.Do("setReplay", map[string]any{"mult": 2.0, "loop": 0.0}); err != nil {
		t.Fatal(err)
	}
	if err := term.Do("setMotion", map[string]any{"mode": "track"}); err != nil {
		t.Fatal(err)
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.Lat != 23.1 || term.Lng != 113.3 {
		t.Fatalf("起点 %v %v", term.Lat, term.Lng)
	}
	m0 := term.Mileage
	for i := 0; i < 5; i++ { // 2 倍速 5 秒 = 回放 10 秒，到第 2 个点
		term.stepMotion(1)
	}
	if math.Abs(term.Lng-113.301) > 1e-9 || math.Abs(term.Lat-23.1) > 1e-9 || term.alt != 20 {
		t.Fatalf("10s 位置 %v %v alt=%d", term.Lat, term.Lng, term.alt)
	}
	if d := (term.Mileage - m0) * 1000; math.Abs(d-102.3) > 2 {
		t.Fatalf("里程增加 %.1f m", d)
	}
	term.stepMotion(1) // 第二段无速度：按距离/时间（111m/10s ≈ 40 km/h），方向向北
	if term.speed < 35 || term.speed > 45 || math.Abs(term.dir) > 1 {
		t.Fatalf("速度 %.1f 方向 %.1f", term.speed, term.dir)
	}
	for i := 0; i < 10; i++ {
		term.stepMotion(1)
	}
	v := term.replayView()
	if v["done"] != true || v["progress"] != 1.0 || term.speed != 0 || math.Abs(term.Lat-23.101) > 1e-9 {
		t.Fatalf("结束: %v speed=%v", v, term.speed)
	}
	// 循环 + 按速度回放（去掉时间戳）
	term.Replay.Loop = true
	for i := range term.Replay.Points {
		term.Replay.Points[i].T = 0
		term.Replay.Points[i].Speed = f64(36) // 10 m/s
	}
	term.prepareReplay()
	term.seekReplay(0)
	term.stepMotion(1)
	if d := distM(23.1, 113.3, term.Lat, term.Lng); math.Abs(d-20) > 0.5 { // 2 倍速 × 10 m/s
		t.Fatalf("按速度回放 1s 走了 %.2f m", d)
	}
	for i := 0; i < 12; i++ { // 总长约 213m，2 倍速约 11s 一圈
		term.stepMotion(1)
	}
	if term.rp.done || term.replayView()["progress"].(float64) > 0.5 {
		t.Fatalf("循环回放应从头开始: %v", term.replayView())
	}
}

func TestRouteAPI(t *testing.T) {
	stateDir = t.TempDir()
	m := NewManager(Config{Phone: "13800000001"})
	defer stopAll(m)
	h := newWebMux(m)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/t/1/route?name=%E6%B5%8B%E8%AF%95", bytes.NewReader([]byte(testGPX))))
	var r map[string]any
	json.Unmarshal(w.Body.Bytes(), &r)
	if r["ok"] != true || r["replay"].(map[string]any)["points"] != 3.0 {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/t/1/route", nil))
	json.Unmarshal(w.Body.Bytes(), &r)
	if r["name"] != "测试" || len(r["points"].([]any)) != 3 {
		t.Fatal(w.Body.String())
	}
	// 状态随终端保存，复制终端时带上路线
	t2, err := m.Add(m.Get("1"), true)
	if err != nil || t2.Replay == nil || len(t2.Replay.Points) != 3 {
		t.Fatal("复制终端应带上路线", err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/t/1/route", bytes.NewReader([]byte("garbage"))))
	json.Unmarshal(w.Body.Bytes(), &r)
	if r["ok"] != false {
		t.Fatal("非法文件应报错")
	}
}
