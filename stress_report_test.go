package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStressReport(t *testing.T) {
	stateDir = t.TempDir()
	p := newMultiPlat(t)
	port := p.ln.Addr().(*net.TCPAddr).Port
	old := stress
	stress = &Stress{}
	defer func() { stress = old }()
	s := stress
	if err := s.Start(StressConfig{Host: "127.0.0.1", Port: port, Count: 20, Ramp: 200, Interval: 1, PhoneStart: "13300000001"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 8*time.Second, func() bool { return s.online.Load() == 20 && s.acks.Load() >= 40 })
	time.Sleep(2200 * time.Millisecond) // 至少两个采样点
	snap := s.Report()
	if snap.Final || snap.PeakOnline != 20 || len(snap.Series) < 2 {
		t.Fatalf("快照: final=%v peak=%d points=%d", snap.Final, snap.PeakOnline, len(snap.Series))
	}
	s.Stop("测试")
	s.wg.Wait()
	list := listReports()
	if len(list) != 1 || list[0].Peak != 20 || list[0].Count != 20 {
		t.Fatalf("%+v", list)
	}
	r, err := loadReport(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	l := r.Latency
	if !r.Final || r.Reason != "测试" || r.RegOK != 20 || r.Timeouts != 0 || r.FailRate != 0 || r.PeakConn < 20 || r.AvgOnline <= 0 ||
		l.Count < 40 || l.P50 < 0 || l.P50 > l.P90 || l.P90 > l.P99 || l.P99 > l.Max || r.DurationS < 2 || len(r.Hist) != len(latBuckets) {
		t.Fatalf("%+v", r)
	}
	html, err := r.HTML()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<svg", "polyline", "JT808 网关压测报告", "13300000001", "<rect"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("HTML 缺少 %q", want)
		}
	}
	if strings.Contains(string(html), "http://") || strings.Contains(string(html), "<script") {
		t.Error("HTML 应自包含（无外部资源、无脚本）")
	}
	if lines := strings.Count(string(r.CSV()), "\n"); lines != len(r.Series)+1 {
		t.Fatalf("CSV 行数 %d", lines)
	}
	// API
	srv := httptest.NewServer(newWebMux(NewManager(Config{Phone: "13800000001"})))
	defer srv.Close()
	get := func(q string) (*httpResp, string) {
		res, err := srv.Client().Get(srv.URL + q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return &httpResp{res.StatusCode, res.Header.Get("Content-Disposition"), res.Header.Get("Content-Type")}, string(b)
	}
	if h, b := get("/api/stress/report?id=current&format=json"); h.code != 200 || !strings.Contains(h.disp, r.ID+".json") {
		t.Fatal(h, b)
	} else {
		var j StressReport
		if json.Unmarshal([]byte(b), &j) != nil || j.ID != r.ID {
			t.Fatal(b[:200])
		}
	}
	if h, _ := get("/api/stress/report?id=" + r.ID + "&format=csv"); h.code != 200 || !strings.Contains(h.ct, "text/csv") {
		t.Fatal(h)
	}
	if h, b := get("/api/stress/report?id=" + r.ID + "&view=1"); h.code != 200 || h.disp != "" || !strings.Contains(b, "<svg") {
		t.Fatal(h)
	}
	if h, _ := get("/api/stress/report?id=../../etc/passwd"); h.code != 404 {
		t.Fatal(h)
	}
	if _, b := get("/api/stress/reports"); !strings.Contains(b, r.ID) {
		t.Fatal(b)
	}
}

type httpResp struct {
	code     int
	disp, ct string
}

func TestReportHelpers(t *testing.T) {
	if niceMax(0) != 1 || niceMax(7) != 10 || niceMax(120) != 200 || niceMax(2400) != 2500 {
		t.Fatal(niceMax(7), niceMax(120), niceMax(2400))
	}
	if quantile([]float32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, .9) != 9 || quantile(nil, .5) != 0 {
		t.Fatal("quantile")
	}
	s := &Stress{}
	for i := 0; i < 2*maxReportPoints+10; i++ {
		s.record(stressSample{T: int64(i), Online: int64(i % 7)})
	}
	if len(s.all) > maxReportPoints || s.stride != 4 || s.peakOn != 6 || s.all[1].T != 4 {
		t.Fatalf("points=%d stride=%d", len(s.all), s.stride)
	}
	for i := 0; i < resCap+500; i++ {
		s.sampleLat(float32(i))
	}
	if len(s.res) != resCap || s.resN != resCap+500 {
		t.Fatal(len(s.res))
	}
}
