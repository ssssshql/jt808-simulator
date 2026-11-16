package main

// 压测报告：压测停止时自动生成（也可运行中手动生成当前快照），保存到 state/stress-reports/（保留最近 20 份）。
// 导出：JSON（完整数据）、CSV（按时间的采样曲线）、HTML（自包含，内联 CSS + SVG 图表，可直接发给别人打开）。

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxReportPoints = 3600 // 全程采样点上限，超过后隔点抽稀
	maxReportFiles  = 20
	resCap          = 100000 // 延迟蓄水池容量
)

type LatencyStats struct {
	Count int64   `json:"count"`
	Avg   float64 `json:"avg"`
	P50   float64 `json:"p50"`
	P90   float64 `json:"p90"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	Max   float64 `json:"max"`
}

type HistBin struct {
	LE    string `json:"le"` // 上限（毫秒），最后一档为 "+Inf"
	Count int64  `json:"count"`
}

type StressReport struct {
	ID          string         `json:"id"`
	App         string         `json:"app"`
	Generated   int64          `json:"generated"`
	Final       bool           `json:"final"` // 压测已结束（false = 运行中的快照）
	Reason      string         `json:"reason,omitempty"`
	Config      StressConfig   `json:"config"`
	Started     int64          `json:"started"`
	Ended       int64          `json:"ended,omitempty"`
	DurationS   float64        `json:"durationS"`
	PeakOnline  int64          `json:"peakOnline"`
	AvgOnline   float64        `json:"avgOnline"`
	PeakConn    int64          `json:"peakConnections"` // 峰值并发连接数
	Spawned     int64          `json:"spawned"`
	Sent        int64          `json:"sent"`
	Recv        int64          `json:"recv"`
	AvgSentRate float64        `json:"avgSentRate"` // 条/秒
	AvgRecvRate float64        `json:"avgRecvRate"`
	PeakSent    float64        `json:"peakSentRate"`
	PeakRecv    float64        `json:"peakRecvRate"`
	Latency     LatencyStats   `json:"latency"` // 毫秒
	Acks        int64          `json:"acks"`
	Nacks       int64          `json:"nacks"`
	Timeouts    int64          `json:"timeouts"`
	Reconnects  int64          `json:"reconnects"`
	RegOK       int64          `json:"regOK"`
	RegFail     int64          `json:"regFail"`
	AuthFail    int64          `json:"authFail"`
	ConnFail    int64          `json:"connFail"`
	SendErrors  int64          `json:"sendErrors"`
	FailRate    float64        `json:"failRate"`     // (应答失败 + 应答超时) / 需应答的消息数
	ConnFailPct float64        `json:"connFailRate"` // 连接失败 / (连接成功 + 连接失败)
	LastError   string         `json:"lastError,omitempty"`
	Hist        []HistBin      `json:"histogram"`
	StrideS     int            `json:"strideS"` // 采样间隔（秒）
	Series      []stressSample `json:"series"`
}

// record 全程采样与峰值（sampleLoop 持有 s.mu 时调用）
func (s *Stress) record(smp stressSample) {
	s.peakOn = max(s.peakOn, smp.Online)
	s.peakConn = max(s.peakConn, smp.Connected)
	s.peakSent = math.Max(s.peakSent, smp.Sent)
	s.peakRecv = math.Max(s.peakRecv, smp.Recv)
	s.onSum += smp.Online
	s.onN++
	if s.stride == 0 {
		s.stride = 1
	}
	if s.tick++; (s.tick-1)%s.stride != 0 {
		return
	}
	s.all = append(s.all, smp)
	if len(s.all) > maxReportPoints {
		keep := s.all[:0]
		for i := 0; i < len(s.all); i += 2 {
			keep = append(keep, s.all[i])
		}
		s.all, s.stride = keep, s.stride*2
	}
}

// sampleLat 蓄水池抽样（算法 R）
func (s *Stress) sampleLat(ms float32) {
	s.resMu.Lock()
	s.resN++
	if len(s.res) < resCap {
		s.res = append(s.res, ms)
	} else if j := rand.Int63n(s.resN); j < resCap {
		s.res[j] = ms
	}
	s.resMu.Unlock()
}

func quantile(sorted []float32, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	return round2(float64(sorted[max(0, min(i, len(sorted)-1))]))
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func pct(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return math.Round(float64(a)/float64(b)*10000) / 100
}

// Report 生成报告（运行中为当前快照）
func (s *Stress) Report() *StressReport {
	s.mu.Lock()
	r := &StressReport{App: versionString(), Generated: time.Now().UnixMilli(), Final: !s.running && !s.ended.IsZero(), Reason: s.reason,
		Config: s.cfg, LastError: s.lastErr, PeakOnline: s.peakOn, PeakConn: s.peakConn, PeakSent: s.peakSent, PeakRecv: s.peakRecv,
		StrideS: max(s.stride, 1), Series: append([]stressSample{}, s.all...)}
	started, ended, running := s.started, s.ended, s.running
	if s.onN > 0 {
		r.AvgOnline = math.Round(float64(s.onSum)/float64(s.onN)*10) / 10
	}
	s.mu.Unlock()
	if !started.IsZero() {
		r.Started = started.UnixMilli()
		end := time.Now()
		if !running && !ended.IsZero() {
			end, r.Ended = ended, ended.UnixMilli()
		}
		r.DurationS = math.Round(end.Sub(started).Seconds()*10) / 10
	}
	r.ID = "stress-" + time.UnixMilli(max(r.Started, 0)).Format("20060102-150405")
	if r.Started == 0 {
		r.ID = "stress-empty"
	}
	r.Spawned, r.Sent, r.Recv = s.spawned.Load(), s.sent.Load(), s.recv.Load()
	r.Acks, r.Nacks, r.Timeouts, r.Reconnects = s.acks.Load(), s.nacks.Load(), s.timeouts.Load(), s.reconnects.Load()
	r.RegOK, r.RegFail, r.AuthFail, r.ConnFail, r.SendErrors = s.regOK.Load(), s.regFail.Load(), s.authFail.Load(), s.connFail.Load(), s.sendErrors.Load()
	if r.DurationS > 0 {
		r.AvgSentRate, r.AvgRecvRate = round2(float64(r.Sent)/r.DurationS), round2(float64(r.Recv)/r.DurationS)
	}
	r.FailRate = pct(r.Nacks+r.Timeouts, r.Acks+r.Nacks+r.Timeouts)
	r.ConnFailPct = pct(r.ConnFail, r.ConnFail+r.RegOK+r.RegFail+r.AuthFail) // 每次成功建立连接后都会注册或鉴权
	// 延迟
	s.resMu.Lock()
	res := append([]float32(nil), s.res...)
	s.resMu.Unlock()
	sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
	l := &r.Latency
	l.Count = s.latN.Load()
	if l.Count > 0 {
		l.Avg = round2(float64(s.latSum.Load()) / float64(l.Count) / 1000)
	}
	l.P50, l.P90, l.P95, l.P99 = quantile(res, .5), quantile(res, .9), quantile(res, .95), quantile(res, .99)
	l.Max = round2(float64(s.latMax.Load()) / 1000)
	for i, b := range latBuckets {
		le := "+Inf"
		if !math.IsInf(b, 1) {
			le = ftoa(b, -1)
		}
		r.Hist = append(r.Hist, HistBin{LE: le, Count: s.hist[i].Load()})
	}
	return r
}

func reportDir() string { return filepath.Join(stateDir, "stress-reports") }

// saveReport 生成并保存最终报告，只保留最近 maxReportFiles 份
func (s *Stress) saveReport() (*StressReport, error) {
	r := s.Report()
	s.mu.Lock()
	s.lastReport = r
	s.mu.Unlock()
	if r.Started == 0 {
		return r, nil
	}
	os.MkdirAll(reportDir(), 0o755)
	b, _ := json.MarshalIndent(r, "", " ")
	if err := os.WriteFile(filepath.Join(reportDir(), r.ID+".json"), b, 0o644); err != nil {
		return r, err
	}
	list := listReports()
	for i := maxReportFiles; i < len(list); i++ {
		os.Remove(filepath.Join(reportDir(), list[i].ID+".json"))
	}
	return r, nil
}

type reportBrief struct {
	ID        string  `json:"id"`
	Started   int64   `json:"started"`
	DurationS float64 `json:"durationS"`
	Count     int     `json:"count"`
	Target    string  `json:"target"`
	Peak      int64   `json:"peakOnline"`
	FailRate  float64 `json:"failRate"`
}

// listReports 已保存的报告（新的在前）
func listReports() []reportBrief {
	ents, _ := os.ReadDir(reportDir())
	var out []reportBrief
	for _, e := range ents {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "stress-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		r, err := loadReport(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, reportBrief{ID: r.ID, Started: r.Started, DurationS: r.DurationS, Count: r.Config.Count,
			Target: fmt.Sprintf("%s:%d/%s", r.Config.Host, r.Config.Port, strings.ToUpper(r.Config.Transport)), Peak: r.PeakOnline, FailRate: r.FailRate})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func loadReport(id string) (*StressReport, error) {
	if id != safeName(id) || !strings.HasPrefix(id, "stress-") {
		return nil, fmt.Errorf("报告 ID 无效")
	}
	b, err := os.ReadFile(filepath.Join(reportDir(), id+".json"))
	if err != nil {
		return nil, fmt.Errorf("报告 %s 不存在", id)
	}
	var r StressReport
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ReportCSV 按时间的采样曲线
func (r *StressReport) CSV() []byte {
	var buf bytes.Buffer
	buf.WriteString("\ufeff")
	w := csv.NewWriter(&buf)
	w.Write([]string{"时间", "已运行(秒)", "发送(条/秒)", "接收(条/秒)", "在线", "连接", "应答延迟P95(ms)"})
	for _, p := range r.Series {
		w.Write([]string{time.UnixMilli(p.T).Format("2006-01-02 15:04:05"), ftoa(float64(p.T-r.Started)/1000, 0), ftoa(p.Sent, 1), ftoa(p.Recv, 1),
			itoa(int(p.Online)), itoa(int(p.Connected)), ftoa(p.LatP95, -1)})
	}
	w.Flush()
	return buf.Bytes()
}

// ---------------- HTML ----------------

type svgSeries struct {
	Name  string
	Color string
	Vals  []float64
}

// svgChart 折线图（自包含 SVG）；xs 为相对秒数
func svgChart(xs []float64, series []svgSeries, unit string) template.HTML {
	const W, H, L, R, T, B = 760.0, 220.0, 56.0, 16.0, 14.0, 30.0
	if len(xs) < 2 {
		return template.HTML(`<p class="muted">采样点不足，无法绘图</p>`)
	}
	maxY := 0.0
	for _, s := range series {
		for _, v := range s.Vals {
			maxY = math.Max(maxY, v)
		}
	}
	maxY = niceMax(maxY)
	x0, x1 := xs[0], xs[len(xs)-1]
	if x1 <= x0 {
		x1 = x0 + 1
	}
	px := func(x float64) float64 { return L + (W-L-R)*(x-x0)/(x1-x0) }
	py := func(y float64) float64 { return H - B - (H-T-B)*y/maxY }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" class="chart" role="img">`, W, H)
	for i := 0; i <= 4; i++ {
		y := maxY * float64(i) / 4
		fmt.Fprintf(&b, `<line x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f" class="grid"/><text x="%.0f" y="%.1f" class="ax" text-anchor="end">%s</text>`,
			L, W-R, py(y), py(y), L-6, py(y)+4, template.HTMLEscapeString(shortNum(y)))
	}
	for i := 0; i <= 5; i++ {
		x := x0 + (x1-x0)*float64(i)/5
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" class="ax" text-anchor="middle">%s</text>`, px(x), H-10, fmtDur(x))
	}
	fmt.Fprintf(&b, `<text x="%.0f" y="%.0f" class="ax">%s</text>`, 4.0, T+4, template.HTMLEscapeString(unit))
	for _, s := range series {
		var pts []string
		for i, v := range s.Vals {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", px(xs[i]), py(v)))
		}
		fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.8" stroke-linejoin="round"/>`, strings.Join(pts, " "), s.Color)
	}
	b.WriteString(`</svg><div class="legend">`)
	for _, s := range series {
		fmt.Fprintf(&b, `<span><i style="background:%s"></i>%s</span>`, s.Color, template.HTMLEscapeString(s.Name))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// svgBars 延迟分布柱状图
func svgBars(h []HistBin) template.HTML {
	const W, H, L, B, T = 760.0, 200.0, 56.0, 34.0, 14.0
	var maxC int64
	for _, x := range h {
		maxC = max(maxC, x.Count)
	}
	if maxC == 0 {
		return template.HTML(`<p class="muted">没有应答延迟数据</p>`)
	}
	top := niceMax(float64(maxC))
	bw := (W - L - 10) / float64(len(h))
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" class="chart" role="img">`, W, H)
	for i, x := range h {
		bh := (H - T - B) * float64(x.Count) / top
		xx := L + float64(i)*bw
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="#5b5bd6" rx="2"><title>≤%s ms：%d</title></rect>`, xx+3, H-B-bh, bw-6, bh, x.LE, x.Count)
		if x.Count > 0 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="ax" text-anchor="middle">%s</text>`, xx+bw/2, H-B-bh-4, shortNum(float64(x.Count)))
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" class="ax" text-anchor="middle">≤%s</text>`, xx+bw/2, H-14, x.LE)
	}
	fmt.Fprintf(&b, `<text x="4" y="%.0f" class="ax">次数</text><text x="%.0f" y="%.0f" class="ax" text-anchor="end">毫秒</text></svg>`, T+4, W-4, H-2)
	return template.HTML(b.String())
}

func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	e := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*e {
			return m * e
		}
	}
	return 10 * e
}

func shortNum(v float64) string {
	switch {
	case v >= 1e6:
		return ftoa(v/1e6, 1) + "M"
	case v >= 1e4:
		return ftoa(v/1e3, 1) + "k"
	case v == math.Trunc(v):
		return ftoa(v, 0)
	}
	return ftoa(v, 1)
}

func fmtDur(sec float64) string {
	s := int(sec)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

var reportTpl = template.Must(template.New("r").Funcs(template.FuncMap{
	"t":   func(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02 15:04:05") },
	"dur": func(s float64) string { return fmtDur(s) },
	"num": func(v any) string { return fmt.Sprint(v) },
}).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>压测报告 {{.R.ID}}</title>
<style>
body{font:14px/1.6 -apple-system,"Segoe UI","Microsoft YaHei",sans-serif;color:#1d1d1f;background:#f4f4f1;margin:0;padding:28px}
main{max-width:860px;margin:auto}h1{font-size:22px;margin:0 0 4px}h2{font-size:15px;margin:0 0 12px}
.sub,.muted{color:#6e6e73;font-size:12.5px}.card{background:#fff;border-radius:14px;padding:16px 18px;margin:14px 0;box-shadow:0 1px 3px rgba(0,0,0,.08)}
.kpis{display:grid;grid-template-columns:repeat(4,1fr);gap:10px;margin:14px 0}.kpis div{background:#fff;border-radius:14px;padding:12px 14px;box-shadow:0 1px 3px rgba(0,0,0,.08)}
.kpis div:first-child{background:#1d1d1f;color:#fff}.kpis span{display:block;font-size:12px;opacity:.7}.kpis b{font-size:24px}.kpis small{font-size:12px;opacity:.7;margin-left:3px}
table{width:100%;border-collapse:collapse;font-size:13px}td,th{padding:6px 8px;border-bottom:1px solid #eee;text-align:left}td.n{text-align:right;font-family:ui-monospace,Consolas,monospace}
.grid2{display:grid;grid-template-columns:1fr 1fr;gap:0 24px}.chart{width:100%;height:auto;display:block}.chart .grid{stroke:#e5e5e5}.chart .ax{font-size:10.5px;fill:#86868b}
.legend{display:flex;gap:14px;font-size:12px;color:#6e6e73;margin-top:4px}.legend i{display:inline-block;width:10px;height:3px;border-radius:2px;margin-right:5px;vertical-align:middle}
.bad{color:#d70015}.tag{display:inline-block;padding:0 8px;border-radius:6px;background:#eee;font-size:12px}
@media print{body{background:#fff;padding:0}.card,.kpis div{box-shadow:none;border:1px solid #ddd}}
</style></head><body><main>
<h1>JT808 网关压测报告</h1>
<div class="sub">{{.R.ID}} · {{if .R.Final}}已结束{{if .R.Reason}}（{{.R.Reason}}）{{end}}{{else}}<span class="tag">运行中快照</span>{{end}} · 生成于 {{t .R.Generated}} · {{.R.App}}</div>
<div class="kpis">
<div><span>峰值在线 / 目标</span><b>{{.R.PeakOnline}}</b><small>/ {{.R.Config.Count}}</small></div>
<div><span>平均在线</span><b>{{.R.AvgOnline}}</b></div>
<div><span>应答延迟 P99</span><b>{{.R.Latency.P99}}</b><small>ms</small></div>
<div><span>失败率</span><b class="{{if gt .R.FailRate 0.0}}bad{{end}}">{{.R.FailRate}}</b><small>%</small></div>
</div>
<div class="card"><h2>配置</h2><div class="grid2"><table>
<tr><td>目标网关</td><td class="n">{{.R.Config.Host}}:{{.R.Config.Port}}/{{.R.Config.Transport}}</td></tr>
<tr><td>协议版本</td><td class="n">JT/T 808-{{.R.Config.Version}}</td></tr>
<tr><td>终端数量</td><td class="n">{{.R.Config.Count}}</td></tr>
<tr><td>起始手机号</td><td class="n">{{.R.Config.PhoneStart}}</td></tr></table><table>
<tr><td>位置汇报 / 心跳间隔</td><td class="n">{{.R.Config.Interval}} s / {{.R.Config.Heartbeat}} s</td></tr>
<tr><td>启动速率</td><td class="n">{{.R.Config.Ramp}} 个/秒</td></tr>
<tr><td>应答超时</td><td class="n">{{.R.Config.AckTimeout}} s</td></tr>
<tr><td>计划时长</td><td class="n">{{if .R.Config.Duration}}{{.R.Config.Duration}} s{{else}}手动停止{{end}}</td></tr></table></div></div>
<div class="card"><h2>结果</h2><div class="grid2"><table>
<tr><td>开始</td><td class="n">{{t .R.Started}}</td></tr>
<tr><td>时长</td><td class="n">{{dur .R.DurationS}}</td></tr>
<tr><td>峰值并发连接</td><td class="n">{{.R.PeakConn}}</td></tr>
<tr><td>累计发送 / 接收</td><td class="n">{{.R.Sent}} / {{.R.Recv}}</td></tr>
<tr><td>平均发送 / 接收速率</td><td class="n">{{.R.AvgSentRate}} / {{.R.AvgRecvRate}} 条/秒</td></tr>
<tr><td>峰值发送 / 接收速率</td><td class="n">{{.R.PeakSent}} / {{.R.PeakRecv}} 条/秒</td></tr>
<tr><td>注册成功 / 失败</td><td class="n">{{.R.RegOK}} / {{.R.RegFail}}</td></tr>
<tr><td>鉴权失败</td><td class="n">{{.R.AuthFail}}</td></tr></table><table>
<tr><td>应答成功 / 失败</td><td class="n">{{.R.Acks}} / {{.R.Nacks}}</td></tr>
<tr><td>应答超时</td><td class="n">{{.R.Timeouts}}</td></tr>
<tr><td>重连</td><td class="n">{{.R.Reconnects}}</td></tr>
<tr><td>连接失败（失败率）</td><td class="n">{{.R.ConnFail}}（{{.R.ConnFailPct}}%）</td></tr>
<tr><td>发送错误</td><td class="n">{{.R.SendErrors}}</td></tr>
<tr><td>应答延迟 平均 / 最大</td><td class="n">{{.R.Latency.Avg}} / {{.R.Latency.Max}} ms</td></tr>
<tr><td>P50 / P90 / P95 / P99</td><td class="n">{{.R.Latency.P50}} / {{.R.Latency.P90}} / {{.R.Latency.P95}} / {{.R.Latency.P99}} ms</td></tr>
<tr><td>延迟样本</td><td class="n">{{.R.Latency.Count}}</td></tr></table></div>
{{if .R.LastError}}<p class="bad">最近错误：{{.R.LastError}}</p>{{end}}
<p class="muted">失败率 =（应答失败 + 应答超时）/ 需要平台应答的消息数；分位数由最多 10 万个延迟样本（蓄水池抽样）计算。</p></div>
<div class="card"><h2>在线与连接</h2>{{.Online}}</div>
<div class="card"><h2>收发速率</h2>{{.Rate}}</div>
<div class="card"><h2>应答延迟 P95（每秒窗口）</h2>{{.Lat}}</div>
<div class="card"><h2>应答延迟分布</h2>{{.Hist}}</div>
<p class="muted">采样间隔 {{.R.StrideS}} 秒，共 {{len .R.Series}} 个点。由 JT808 终端模拟器生成。</p>
</main></body></html>`))

func (r *StressReport) HTML() ([]byte, error) {
	var xs []float64
	on, conn, sent, recv, lat := svgSeries{Name: "在线", Color: "#1fae5b"}, svgSeries{Name: "连接", Color: "#8e8e93"},
		svgSeries{Name: "发送", Color: "#0a84ff"}, svgSeries{Name: "接收", Color: "#f97316"}, svgSeries{Name: "P95", Color: "#5b5bd6"}
	for _, p := range r.Series {
		xs = append(xs, float64(p.T-r.Started)/1000)
		on.Vals, conn.Vals = append(on.Vals, float64(p.Online)), append(conn.Vals, float64(p.Connected))
		sent.Vals, recv.Vals, lat.Vals = append(sent.Vals, p.Sent), append(recv.Vals, p.Recv), append(lat.Vals, p.LatP95)
	}
	var buf bytes.Buffer
	err := reportTpl.Execute(&buf, map[string]any{"R": r, "Online": svgChart(xs, []svgSeries{on, conn}, "个"),
		"Rate": svgChart(xs, []svgSeries{sent, recv}, "条/秒"), "Lat": svgChart(xs, []svgSeries{lat}, "ms"), "Hist": svgBars(r.Hist)})
	return buf.Bytes(), err
}

// registerStressReportAPI
//
//	GET /api/stress/reports                      已保存的报告列表
//	GET /api/stress/report?id=current|<ID>&format=html|json|csv[&save=1]   current = 当前（运行中为快照，否则为最近一次）
func registerStressReportAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/stress/reports", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{"reports": listReports()})
	})
	mux.HandleFunc("GET /api/stress/report", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id := q.Get("id")
		var rep *StressReport
		var err error
		if id == "" || id == "current" {
			stress.mu.Lock()
			running, last := stress.running, stress.lastReport
			stress.mu.Unlock()
			if running || last == nil {
				rep = stress.Report()
			} else {
				rep = last
			}
			if rep.Started == 0 {
				err = fmt.Errorf("还没有运行过压力测试")
			}
		} else {
			rep, err = loadReport(id)
		}
		if err != nil {
			writeJSON(w, 404, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		var data []byte
		name, ct := rep.ID, ""
		switch q.Get("format") {
		case "", "html":
			data, err = rep.HTML()
			name, ct = name+".html", "text/html; charset=utf-8"
			if q.Get("view") == "1" { // 页面内预览（不作为附件）
				w.Header().Set("Content-Type", ct)
				w.Write(data)
				return
			}
		case "json":
			data, _ = json.MarshalIndent(rep, "", " ")
			name, ct = name+".json", "application/json; charset=utf-8"
		case "csv":
			data = rep.CSV()
			name, ct = name+".csv", "text/csv; charset=utf-8"
		default:
			err = fmt.Errorf("format 只能是 html/json/csv")
		}
		if err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		serveDownload(w, r, name, ct, data)
	})
}
