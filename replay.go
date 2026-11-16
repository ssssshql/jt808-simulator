package main

// 轨迹回放：导入 GPX / CSV 路线作为运动来源（运动模式 track），支持倍速与循环。
// 路线点带时间时按时间戳回放（速度取点上的速度或相邻点距离/时间）；不带时间时按点上的速度（或目标速度）沿路线行驶。

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	maxRoutePoints = 100000
	maxRouteBytes  = 16 << 20
)

type RoutePoint struct {
	Lat   float64  `json:"lat"`
	Lng   float64  `json:"lng"`
	Speed *float64 `json:"speed,omitempty"` // km/h
	T     int64    `json:"t,omitempty"`     // 毫秒时间戳（0 = 无）
	Alt   *float64 `json:"alt,omitempty"`   // 米
	Dir   *float64 `json:"dir,omitempty"`   // 度
}

// Replay 导入的路线与回放设置（随终端状态保存）
type Replay struct {
	Name   string       `json:"name"`
	Format string       `json:"format"`
	Points []RoutePoint `json:"points"`
	Mult   float64      `json:"mult"`
	Loop   bool         `json:"loop"`
	Rev    int64        `json:"rev"` // 导入时间（毫秒），页面据此判断是否需要重新获取路线
}

// replayRun 回放游标（不保存）
type replayRun struct {
	ready   bool
	cum     []float64 // 各点累计距离（米）
	times   []float64 // 各点相对时间（秒）；nil = 按速度回放
	seg     int
	dist    float64 // 按速度回放：已行驶距离（米）
	vt      float64 // 按时间回放：已回放时间（秒）
	done    bool
	elapsed float64 // 实际回放秒数（不含倍速）
}

func f64(v float64) *float64 { return &v }

// ---------------- 解析 ----------------

// ParseRoute 自动识别 GPX（以 < 开头）或 CSV
func ParseRoute(data []byte, format string) ([]RoutePoint, string, error) {
	if len(data) > maxRouteBytes {
		return nil, "", fmt.Errorf("文件过大（最大 %d MB）", maxRouteBytes>>20)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if format == "" {
		if bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
			format = "gpx"
		} else {
			format = "csv"
		}
	}
	var pts []RoutePoint
	var err error
	switch format {
	case "gpx":
		pts, err = parseGPX(data)
	case "csv":
		pts, err = parseCSV(data)
	default:
		return nil, "", fmt.Errorf("不支持的格式 %s（gpx/csv）", format)
	}
	if err != nil {
		return nil, format, err
	}
	if len(pts) < 2 {
		return nil, format, fmt.Errorf("有效轨迹点不足 2 个")
	}
	if len(pts) > maxRoutePoints {
		return nil, format, fmt.Errorf("轨迹点过多（%d，最多 %d）", len(pts), maxRoutePoints)
	}
	return pts, format, nil
}

func validLL(lat, lng float64) bool {
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180 && !(lat == 0 && lng == 0) && !math.IsNaN(lat) && !math.IsNaN(lng)
}

// parseGPX 读取 trkpt（没有则 rtept，再没有则 wpt）：lat/lon、ele、time、speed（m/s，含 gpxtpx 扩展）、course
func parseGPX(data []byte) ([]RoutePoint, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	byKind := map[string][]RoutePoint{}
	var cur *RoutePoint
	var kind, field string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("GPX 解析失败: %v", err)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(e.Name.Local)
			switch {
			case name == "trkpt" || name == "rtept" || name == "wpt":
				var lat, lng float64
				var e1, e2 error = fmt.Errorf("缺少"), fmt.Errorf("缺少")
				for _, a := range e.Attr {
					switch strings.ToLower(a.Name.Local) {
					case "lat":
						lat, e1 = strconv.ParseFloat(strings.TrimSpace(a.Value), 64)
					case "lon", "lng":
						lng, e2 = strconv.ParseFloat(strings.TrimSpace(a.Value), 64)
					}
				}
				if e1 == nil && e2 == nil && validLL(lat, lng) {
					cur, kind = &RoutePoint{Lat: lat, Lng: lng}, name
				}
			case cur != nil:
				field = name
			}
		case xml.CharData:
			if cur == nil || field == "" {
				continue
			}
			v := strings.TrimSpace(string(e))
			if v == "" {
				continue
			}
			switch field {
			case "ele":
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					cur.Alt = f64(f)
				}
			case "time":
				if ms, ok := parseTime(v); ok {
					cur.T = ms
				}
			case "speed":
				if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
					cur.Speed = f64(f * 3.6)
				}
			case "course":
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					cur.Dir = f64(math.Mod(f+360, 360))
				}
			}
		case xml.EndElement:
			name := strings.ToLower(e.Name.Local)
			if cur != nil && name == kind {
				byKind[kind] = append(byKind[kind], *cur)
				cur, field = nil, ""
				if len(byKind[kind]) > maxRoutePoints {
					return nil, fmt.Errorf("轨迹点过多（最多 %d）", maxRoutePoints)
				}
			} else if name == field {
				field = ""
			}
		}
	}
	for _, k := range []string{"trkpt", "rtept", "wpt"} {
		if len(byKind[k]) > 0 {
			return byKind[k], nil
		}
	}
	return nil, fmt.Errorf("GPX 中没有轨迹点（trkpt/rtept/wpt）")
}

var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006/01/02 15:04:05", "2006-01-02 15:04:05.000",
	"2006/1/2 15:04:05", "20060102150405", "060102150405"}

// parseTime 时间：Unix 秒/毫秒、RFC3339、常见日期格式、BCD 风格 YYMMDDhhmmss。只用于计算相对时间
func parseTime(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if len(s) != 12 && len(s) != 14 { // 12/14 位纯数字按 YYMMDDhhmmss / YYYYMMDDhhmmss 解析
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			switch {
			case f > 1e11: // 毫秒
				return int64(f), true
			case f > 1e8: // 秒
				return int64(f * 1000), true
			default:
				return 0, false
			}
		}
	}
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

// parseCSV 列：lng,lat[,speed,time,alt,dir]（速度 km/h）。首行为表头时按列名识别（lng/lon/longitude、lat/latitude、
// speed、time/timestamp、alt/ele/altitude、dir/course/direction/heading），分隔符支持逗号 / 分号 / 制表符
func parseCSV(data []byte) ([]RoutePoint, error) {
	first, _, _ := bufio.NewReader(bytes.NewReader(data)).ReadLine()
	sep := ','
	if !bytes.ContainsRune(first, ',') {
		if bytes.ContainsRune(first, ';') {
			sep = ';'
		} else if bytes.ContainsRune(first, '\t') {
			sep = '\t'
		}
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.TrimLeadingSpace, r.Comment = sep, -1, true, '#'
	col := map[string]int{"lng": 0, "lat": 1, "speed": 2, "time": 3, "alt": 4, "dir": 5}
	alias := map[string]string{"lng": "lng", "lon": "lng", "long": "lng", "longitude": "lng", "经度": "lng", "lat": "lat", "latitude": "lat", "纬度": "lat",
		"speed": "speed", "速度": "speed", "time": "time", "timestamp": "time", "datetime": "time", "时间": "time", "alt": "alt", "ele": "alt",
		"altitude": "alt", "elevation": "alt", "高程": "alt", "海拔": "alt", "dir": "dir", "course": "dir", "direction": "dir", "heading": "dir", "bearing": "dir", "方向": "dir"}
	var pts []RoutePoint
	line, bad := 0, 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("CSV 第 %d 行: %v", line, err)
		}
		get := func(k string) string {
			if i, ok := col[k]; ok && i >= 0 && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		lng, e1 := strconv.ParseFloat(get("lng"), 64)
		lat, e2 := strconv.ParseFloat(get("lat"), 64)
		if e1 != nil || e2 != nil {
			if len(pts) == 0 && bad == 0 { // 表头
				named := map[string]int{}
				for i, h := range rec {
					if k, ok := alias[strings.ToLower(strings.TrimSpace(h))]; ok {
						named[k] = i
					}
				}
				if _, a := named["lng"]; a {
					if _, b := named["lat"]; b {
						col = map[string]int{"speed": -1, "time": -1, "alt": -1, "dir": -1}
						for k, i := range named {
							col[k] = i
						}
					}
				}
				bad = -1 // 表头只识别一次
				continue
			}
			bad++
			continue
		}
		if !validLL(lat, lng) {
			bad++
			continue
		}
		p := RoutePoint{Lat: lat, Lng: lng}
		if v, err := strconv.ParseFloat(get("speed"), 64); err == nil && v >= 0 {
			p.Speed = f64(v)
		}
		if ms, ok := parseTime(get("time")); ok {
			p.T = ms
		}
		if v, err := strconv.ParseFloat(get("alt"), 64); err == nil {
			p.Alt = f64(v)
		}
		if v, err := strconv.ParseFloat(get("dir"), 64); err == nil {
			p.Dir = f64(math.Mod(v+360, 360))
		}
		pts = append(pts, p)
		if len(pts) > maxRoutePoints {
			return nil, fmt.Errorf("轨迹点过多（最多 %d）", maxRoutePoints)
		}
	}
	return pts, nil
}

// ---------------- 回放 ----------------

// prepareReplay 计算累计距离与相对时间。调用方持有 t.mu
func (t *Terminal) prepareReplay() {
	t.rp = replayRun{}
	r := t.Replay
	if r == nil || len(r.Points) < 2 {
		return
	}
	n := len(r.Points)
	t.rp.cum = make([]float64, n)
	for i := 1; i < n; i++ {
		a, b := r.Points[i-1], r.Points[i]
		t.rp.cum[i] = t.rp.cum[i-1] + distM(a.Lat, a.Lng, b.Lat, b.Lng)
	}
	hasTime := r.Points[0].T > 0
	for i := 1; i < n && hasTime; i++ {
		if r.Points[i].T <= 0 || r.Points[i].T < r.Points[i-1].T {
			hasTime = false
		}
	}
	if hasTime && r.Points[n-1].T > r.Points[0].T {
		t.rp.times = make([]float64, n)
		for i := range r.Points {
			t.rp.times[i] = float64(r.Points[i].T-r.Points[0].T) / 1000
		}
	}
	t.rp.ready = true
}

func (t *Terminal) replayMult() float64 {
	if t.Replay == nil || t.Replay.Mult <= 0 {
		return 1
	}
	return t.Replay.Mult
}

// seekReplay 跳到进度 frac（0~1）。调用方持有 t.mu
func (t *Terminal) seekReplay(frac float64) {
	if !t.rp.ready {
		t.prepareReplay()
	}
	if !t.rp.ready {
		return
	}
	frac = math.Max(0, math.Min(1, frac))
	t.rp.seg, t.rp.done, t.rp.elapsed = 0, false, 0
	if t.rp.times != nil {
		t.rp.vt = frac * t.rp.times[len(t.rp.times)-1]
	} else {
		t.rp.dist = frac * t.rp.cum[len(t.rp.cum)-1]
	}
	t.applyReplayPos(false)
	t.track = nil
}

// stepReplay 回放 dt 秒（乘以倍速）。调用方持有 t.mu
func (t *Terminal) stepReplay(dt float64) {
	if !t.rp.ready {
		t.prepareReplay()
	}
	if !t.rp.ready {
		t.speed = 0
		return
	}
	if t.StatusOn&1 == 0 || t.rp.done { // ACC 关：暂停
		t.speed = 0
		return
	}
	adv := dt * t.replayMult()
	t.rp.elapsed += dt
	pts := t.Replay.Points
	n := len(pts)
	if t.rp.times != nil {
		t.rp.vt += adv
		if end := t.rp.times[n-1]; t.rp.vt >= end {
			if t.Replay.Loop {
				t.rp.vt, t.rp.seg = math.Mod(t.rp.vt, end), 0
				t.track = nil
			} else {
				t.rp.vt, t.rp.done = end, true
			}
		}
	} else {
		v := t.motion.Speed
		if s := pts[min(t.rp.seg, n-1)].Speed; s != nil && *s > 0 {
			v = *s
		}
		t.rp.dist += v / 3.6 * adv
		if end := t.rp.cum[n-1]; t.rp.dist >= end {
			if t.Replay.Loop && end > 0 {
				t.rp.dist, t.rp.seg = math.Mod(t.rp.dist, end), 0
				t.track = nil
			} else {
				t.rp.dist, t.rp.done = end, true
			}
		}
	}
	t.applyReplayPos(true)
}

// applyReplayPos 按游标插值出位置/速度/方向/海拔
func (t *Terminal) applyReplayPos(move bool) {
	pts := t.Replay.Points
	n := len(pts)
	var frac float64
	if t.rp.times != nil {
		tm := t.rp.times
		if t.rp.seg >= n-1 || tm[t.rp.seg] > t.rp.vt {
			t.rp.seg = 0
		}
		for t.rp.seg < n-2 && tm[t.rp.seg+1] <= t.rp.vt {
			t.rp.seg++
		}
		if d := tm[t.rp.seg+1] - tm[t.rp.seg]; d > 0 {
			frac = (t.rp.vt - tm[t.rp.seg]) / d
		} else {
			frac = 1
		}
	} else {
		c := t.rp.cum
		if t.rp.seg >= n-1 || c[t.rp.seg] > t.rp.dist {
			t.rp.seg = 0
		}
		for t.rp.seg < n-2 && c[t.rp.seg+1] <= t.rp.dist {
			t.rp.seg++
		}
		if d := c[t.rp.seg+1] - c[t.rp.seg]; d > 0 {
			frac = (t.rp.dist - c[t.rp.seg]) / d
		} else {
			frac = 1
		}
	}
	frac = math.Max(0, math.Min(1, frac))
	a, b := pts[t.rp.seg], pts[t.rp.seg+1]
	lat, lng := a.Lat+(b.Lat-a.Lat)*frac, a.Lng+(b.Lng-a.Lng)*frac
	segLen := t.rp.cum[t.rp.seg+1] - t.rp.cum[t.rp.seg]
	// 速度：点上的速度插值；否则按时间回放时用距离/时间；按速度回放时用所用速度
	switch {
	case t.rp.done:
		t.speed = 0
	case a.Speed != nil && b.Speed != nil:
		t.speed = *a.Speed + (*b.Speed-*a.Speed)*frac
	case a.Speed != nil:
		t.speed = *a.Speed
	case t.rp.times != nil:
		if d := t.rp.times[t.rp.seg+1] - t.rp.times[t.rp.seg]; d > 0 {
			t.speed = segLen / d * 3.6
		} else {
			t.speed = 0
		}
	default:
		t.speed = t.motion.Speed
	}
	t.speed = math.Max(0, math.Min(t.speed, 300))
	if a.Dir != nil {
		t.dir = *a.Dir
	} else if segLen > 0.5 {
		t.dir = bearing(a.Lat, a.Lng, b.Lat, b.Lng)
	}
	if a.Alt != nil {
		alt := *a.Alt
		if b.Alt != nil {
			alt += (*b.Alt - alt) * frac
		}
		t.alt = int(math.Round(alt))
	}
	if move {
		if d := distM(t.Lat, t.Lng, lat, lng); d > 0 && d < 50000 {
			t.Mileage += d / 1000
			t.Fuel = math.Max(0, t.Fuel-d/1000*0.3)
		}
		t.track = append(t.track, [2]float64{lng, lat})
		if len(t.track) > 300 {
			t.track = t.track[len(t.track)-300:]
		}
	}
	t.Lat, t.Lng = lat, lng
	t.markDirty()
}

// replayView 页面显示的回放状态。调用方持有 t.mu
func (t *Terminal) replayView() map[string]any {
	r := t.Replay
	if r == nil || len(r.Points) < 2 {
		return map[string]any{"loaded": false}
	}
	if !t.rp.ready {
		t.prepareReplay()
	}
	n := len(r.Points)
	total := t.rp.cum[n-1]
	v := map[string]any{"loaded": true, "name": r.Name, "format": r.Format, "points": n, "distanceKm": math.Round(total/10) / 100,
		"mult": t.replayMult(), "loop": r.Loop, "done": t.rp.done, "rev": r.Rev, "hasTime": t.rp.times != nil, "elapsed": int(t.rp.elapsed)}
	prog := 0.0
	if t.rp.times != nil {
		end := t.rp.times[n-1]
		v["durationS"] = int(end)
		if end > 0 {
			prog = t.rp.vt / end
		}
	} else if total > 0 {
		prog = t.rp.dist / total
	}
	v["progress"] = math.Round(prog*1000) / 1000
	return v
}

// routeView 地图用的路线（超过 2000 点时等间隔抽稀）
func (t *Terminal) routeView() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.Replay
	if r == nil {
		return map[string]any{"loaded": false, "points": [][2]float64{}}
	}
	step := max(1, len(r.Points)/2000)
	pts := make([][2]float64, 0, len(r.Points)/step+2)
	for i := 0; i < len(r.Points); i += step {
		pts = append(pts, [2]float64{r.Points[i].Lat, r.Points[i].Lng})
	}
	if last := r.Points[len(r.Points)-1]; (len(r.Points)-1)%step != 0 {
		pts = append(pts, [2]float64{last.Lat, last.Lng})
	}
	return map[string]any{"loaded": true, "name": r.Name, "rev": r.Rev, "points": pts}
}

// importRoute 导入路线（替换已有路线，游标回到起点）
func (t *Terminal) importRoute(name, format string, data []byte) (map[string]any, error) {
	pts, format, err := ParseRoute(data, format)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "导入路线"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	mult, loop := 1.0, true
	if t.Replay != nil {
		mult, loop = t.replayMult(), t.Replay.Loop
	}
	t.Replay = &Replay{Name: name, Format: format, Points: pts, Mult: mult, Loop: loop, Rev: time.Now().UnixMilli()}
	t.prepareReplay()
	if t.motion.Mode == "track" {
		t.seekReplay(0)
	}
	t.markDirty()
	v := t.replayView()
	t.logf("已导入轨迹「%s」：%s %d 点，%.2f km%s", name, strings.ToUpper(format), len(pts), v["distanceKm"],
		map[bool]string{true: "，带时间戳（按时间回放）", false: "，无时间戳（按速度回放）"}[t.rp.times != nil])
	return v, nil
}

// replayAction setReplay：{mult, loop, restart, seek}；clearReplay
func (t *Terminal) replayAction(typ string, p map[string]any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if typ == "clearReplay" {
		t.Replay, t.rp = nil, replayRun{}
		if t.motion.Mode == "track" {
			t.motion.Mode = "stop"
		}
		t.markDirty()
		return nil
	}
	if t.Replay == nil {
		return fmt.Errorf("请先导入轨迹（GPX / CSV）")
	}
	if _, ok := p["mult"]; ok {
		m, err := strconv.ParseFloat(pstr(p, "mult", "1"), 64)
		if err != nil || m < 0.1 || m > 100 {
			return fmt.Errorf("倍速范围 0.1~100")
		}
		t.Replay.Mult = m
	}
	if _, ok := p["loop"]; ok {
		t.Replay.Loop = pint(p, "loop", 0) == 1
		if t.Replay.Loop && t.rp.done {
			t.rp.done = false
		}
	}
	if pint(p, "restart", 0) == 1 {
		t.seekReplay(0)
	}
	if _, ok := p["seek"]; ok {
		f, err := strconv.ParseFloat(pstr(p, "seek", "0"), 64)
		if err != nil {
			return fmt.Errorf("seek 无效")
		}
		t.seekReplay(f)
	}
	t.markDirty()
	return nil
}
