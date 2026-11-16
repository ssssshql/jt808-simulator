package main

// 区域/路线：8600~8607 设置删除，8608 查询，进出区域/路线报警与区域限速

import (
	"encoding/hex"
	"fmt"
	"math"
	"time"
)

var areaKindName = map[int]string{1: "圆形区域", 2: "矩形区域", 3: "多边形区域", 4: "路线"}

// parseAreaItem 解析一个区域项；loose=true 时按网关实际编码（条件字段无条件存在）
func parseAreaItem(kind, ver int, r *reader, loose bool) Area {
	start := r.p
	a := Area{Kind: kind, ID: r.u32(), Attr: r.u16()}
	timeF := func() {
		if a.Attr&1 != 0 || (loose && kind == 4) {
			a.Start, a.End = r.time6(), r.time6()
		}
	}
	speedF := func(night bool) {
		if a.Attr&2 != 0 || (loose && kind == 3) {
			a.MaxSpeed, a.Duration = r.u16(), r.u8()
			if night && ver >= V2019 {
				a.Night = r.u16()
			}
		}
	}
	ll := func() [2]float64 { lat := float64(r.u32()) / 1e6; return [2]float64{lat, float64(r.u32()) / 1e6} }
	switch kind {
	case 1:
		a.Points = [][2]float64{ll()}
		a.Radius = r.u32()
		timeF()
		speedF(true)
	case 2:
		a.Points = [][2]float64{ll(), ll()}
		timeF()
		speedF(true)
	case 3:
		timeF()
		if a.Attr&2 != 0 || loose {
			a.MaxSpeed, a.Duration = r.u16(), r.u8()
		}
		n := r.u16()
		for i := 0; i < n && r.err == nil; i++ {
			a.Points = append(a.Points, ll())
		}
		if ver >= V2019 && (a.Attr&2 != 0 || loose) {
			a.Night = r.u16()
		}
	case 4:
		timeF()
		n := r.u16()
		for i := 0; i < n && r.err == nil; i++ {
			sg := RouteSeg{PointID: r.u32(), ID: r.u32()}
			p := ll()
			sg.Lat, sg.Lng = p[0], p[1]
			a.Points = append(a.Points, p)
			sg.Width, sg.Attr = r.u8(), r.u8()
			if sg.Attr&1 != 0 || loose { // 路段行驶过长阈值 / 不足阈值
				sg.Max, sg.Min = r.u16(), r.u16()
			}
			if sg.Attr&2 != 0 || loose { // 路段最高速度 / 超速持续时间 / 夜间最高速度(2019)
				sg.MaxSpeed, sg.Dur = r.u16(), r.u8()
				if ver >= V2019 {
					sg.Night = r.u16()
				}
			}
			a.Segs = append(a.Segs, sg)
		}
	}
	if ver >= V2019 {
		a.Name = r.wstr()
	}
	if r.err == nil {
		a.Raw = hex.EncodeToString(r.b[start:r.p])
	}
	return a
}

// parseAreas 先按标准解析，长度对不上再按网关编码解析
func parseAreas(id, ver int, body []byte) (mode int, list []Area, err error) {
	kind := map[int]int{0x8600: 1, 0x8602: 2, 0x8604: 3, 0x8606: 4}[id]
	for _, loose := range []bool{false, true} {
		r := newReader(body)
		list = nil
		n := 1
		if kind <= 2 {
			mode, n = r.u8(), r.u8()
		}
		for i := 0; i < n && r.err == nil; i++ {
			list = append(list, parseAreaItem(kind, ver, r, loose))
		}
		if r.err == nil && r.left() == 0 {
			return mode, list, nil
		}
		err = r.err
		if err == nil {
			err = fmt.Errorf("多余 %d 字节", r.left())
		}
	}
	return 0, nil, err
}

// applyAreas 设置属性：0 更新(先清空同类) 1 追加 2 修改。调用方持有 t.mu
func (t *Terminal) applyAreas(kind, mode int, list []Area) {
	if kind > 2 || mode == 0 {
		if kind <= 2 {
			t.deleteAreas(kind, nil)
		} else {
			for _, a := range list {
				t.deleteAreas(kind, []int{a.ID})
			}
		}
	}
	for _, a := range list {
		t.deleteAreas(kind, []int{a.ID})
		t.Areas = append(t.Areas, a)
	}
	t.markDirty()
}

func (t *Terminal) deleteAreas(kind int, ids []int) int {
	keep, n := t.Areas[:0], 0
	for _, a := range t.Areas {
		del := a.Kind == kind
		if del && len(ids) > 0 {
			del = false
			for _, id := range ids {
				del = del || id == a.ID
			}
		}
		if del {
			n++
			delete(t.inArea, a.Kind<<32|a.ID)
		} else {
			keep = append(keep, a)
		}
	}
	t.Areas = keep
	t.markDirty()
	return n
}

func (a *Area) active(now time.Time) bool {
	if a.Attr&1 == 0 || a.Start.IsZero() && a.End.IsZero() {
		return true
	}
	return (a.Start.IsZero() || !now.Before(a.Start)) && (a.End.IsZero() || !now.After(a.End))
}

func (a *Area) contains(lat, lng float64) bool {
	switch a.Kind {
	case 1:
		return distM(lat, lng, a.Points[0][0], a.Points[0][1]) <= float64(a.Radius)
	case 2:
		p := a.Points
		return lat <= math.Max(p[0][0], p[1][0]) && lat >= math.Min(p[0][0], p[1][0]) &&
			lng >= math.Min(p[0][1], p[1][1]) && lng <= math.Max(p[0][1], p[1][1])
	case 3:
		return inPolygon(lat, lng, a.Points)
	case 4: // 距任一拐点 50m 内视为在路线上（简化）
		for _, p := range a.Points {
			if distM(lat, lng, p[0], p[1]) <= 50 {
				return true
			}
		}
	}
	return false
}

// RouteSeg 路线拐点及其所属路段（拐点 i 到拐点 i+1 为路段 i，路段属性随拐点 i 下发）
type RouteSeg struct {
	PointID  int     `json:"pointId"`
	ID       int     `json:"id"` // 路段 ID
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Width    int     `json:"width"` // 路段宽度 m
	Attr     int     `json:"attr"`  // bit0 行驶时间 bit1 限速
	Max      int     `json:"max"`   // 路段行驶过长阈值 s
	Min      int     `json:"min"`   // 路段行驶不足阈值 s
	MaxSpeed int     `json:"maxSpeed"`
	Dur      int     `json:"dur"`
	Night    int     `json:"night"`
}

type segVisit struct {
	idx   int
	since time.Time
}

// segDist 点到线段距离（米，局部平面近似）
func segDist(lat, lng float64, a, b RouteSeg) float64 {
	kx := 111320 * math.Cos(lat*math.Pi/180)
	ax, ay := (a.Lng-lng)*kx, (a.Lat-lat)*111320
	bx, by := (b.Lng-lng)*kx, (b.Lat-lat)*111320
	dx, dy := bx-ax, by-ay
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/l))
	}
	x, y := ax+t*dx, ay+t*dy
	return math.Hypot(x, y)
}

// currentSeg 车辆所在路段下标，-1 表示不在路线上
func (a *Area) currentSeg(lat, lng float64) int {
	best, bestD := -1, math.MaxFloat64
	for i := 0; i+1 < len(a.Segs); i++ {
		tol := math.Max(float64(a.Segs[i].Width), 30)
		if d := segDist(lat, lng, a.Segs[i], a.Segs[i+1]); d <= tol && d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// evalSegments 路段行驶时间不足/过长（报警位 bit22 + 附加信息 0x13）：驶离路段时，
// 若该路段属性 bit0=1，行驶时间 > 过长阈值记「过长」，< 不足阈值记「不足」。调用方持有 t.mu
func (t *Terminal) evalSegments(now time.Time) {
	for i := range t.Areas {
		a := &t.Areas[i]
		if a.Kind != 4 || len(a.Segs) < 2 || !a.active(now) {
			delete(t.segIn, a.ID)
			continue
		}
		cur := a.currentSeg(t.Lat, t.Lng)
		v := t.segIn[a.ID]
		if v != nil && v.idx == cur {
			continue
		}
		if v != nil && v.idx < len(a.Segs) { // 驶离上一路段
			sg := a.Segs[v.idx]
			sec := int(now.Sub(v.since).Seconds())
			result := -1
			if sg.Attr&1 != 0 {
				if sg.Max > 0 && sec > sg.Max {
					result = 1
				} else if sg.Min > 0 && sec < sg.Min {
					result = 0
				}
			}
			if result >= 0 {
				t.logf("路线 %d 路段 %d 行驶 %ds，%s（阈值 不足<%ds 过长>%ds）", a.ID, sg.ID, sec,
					map[int]string{0: "行驶时间不足", 1: "行驶时间过长"}[result], sg.Min, sg.Max)
				t.alarmAuto |= 1 << 22
				t.alarmClr &^= 1 << 22
				t.extra = cat(t.extra, []byte{0x13, 7}, u32(sg.ID), u16(min(sec, 0xFFFF)), []byte{byte(result)})
				t.forceRpt = true
			}
		}
		if cur < 0 {
			delete(t.segIn, a.ID)
		} else {
			t.segIn[a.ID] = &segVisit{idx: cur, since: now}
		}
	}
}

// evalAreas 进出区域/路线报警（区域属性 bit3 进区域报警给平台，bit5 出区域报警给平台）与区域限速。调用方持有 t.mu
func (t *Terminal) evalAreas() {
	now := time.Now()
	areaOver := false
	for i := range t.Areas {
		a := &t.Areas[i]
		if !a.active(now) || len(a.Points) == 0 {
			continue
		}
		key := a.Kind<<32 | a.ID
		in := a.contains(t.Lat, t.Lng)
		was, known := t.inArea[key]
		t.inArea[key] = in
		if known && in != was {
			dir := 0 // 0 进 1 出
			if !in {
				dir = 1
			}
			t.logf("%s %d「%s」：%s", areaKindName[a.Kind], a.ID, a.Name, map[int]string{0: "驶入", 1: "驶出"}[dir])
			if in && a.Attr&(1<<3) != 0 || !in && a.Attr&(1<<5) != 0 {
				bit := 20
				if a.Kind == 4 {
					bit = 21
				}
				t.alarmAuto |= 1 << bit
				t.alarmClr &^= 1 << bit
				t.extra = cat(t.extra, []byte{0x12, 6, byte(a.Kind)}, u32(a.ID), []byte{byte(dir)})
				t.forceRpt = true
			}
		}
		if in && a.Attr&2 != 0 && a.MaxSpeed > 0 && t.speed > float64(a.MaxSpeed) {
			areaOver = true
			if t.areaOverSec++; t.areaOverSec == max(a.Duration, 1) {
				t.extra = cat(t.extra, []byte{0x11, 5, byte(a.Kind)}, u32(a.ID))
				t.forceRpt = true
			}
		}
	}
	if !areaOver {
		t.areaOverSec = 0
	}
	if t.areaOverSec > 0 {
		t.alarmAuto |= 1 << 1
	}
	t.evalSegments(now)
}

// areaQuery 0608 查询区域应答体
func (t *Terminal) areaQuery(kind int, ids []int) []byte {
	var items []byte
	n := 0
	for _, a := range t.Areas {
		if a.Kind != kind {
			continue
		}
		ok := len(ids) == 0
		for _, id := range ids {
			ok = ok || id == a.ID
		}
		if b, err := hex.DecodeString(a.Raw); ok && err == nil {
			items = append(items, b...)
			n++
		}
	}
	return cat([]byte{byte(kind)}, u32(n), items)
}
