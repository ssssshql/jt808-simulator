package main

// 车辆运动模拟（每秒）+ 自动报警判定（超速/疲劳/当天累计/超时停车）

import (
	"math"
	"math/rand"
	"time"
)

type Motion struct {
	Mode   string  `json:"mode"`  // stop / drive / circle / route / track（轨迹回放）
	Speed  float64 `json:"speed"` // 目标速度 km/h
	center [2]float64
	wp     int
}

// 默认巡航路线（广州天河一圈），平台未下发路线时 route 模式使用
var defaultRoute = [][2]float64{{23.1364, 113.3220}, {23.1366, 113.3396}, {23.1250, 113.3405}, {23.1195, 113.3300},
	{23.1199, 113.3180}, {23.1290, 113.3150}}

func (t *Terminal) motionLoop() {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-t.quit:
			return
		case <-tk.C:
		}
		t.mu.Lock()
		t.stepMotion(1)
		t.evalAlarms(1)
		t.evalAreas()
		t.mu.Unlock()
		t.reportTick()
		t.photoTick()
	}
}

// stepMotion 调用方持有 t.mu
func (t *Terminal) stepMotion(dt float64) {
	if t.motion.Mode == "track" { // 轨迹回放：位置/速度/方向/海拔来自导入的路线
		t.stepReplay(dt)
		if t.alarmWord()&(1<<4|1<<5) == 0 {
			t.sats = 10 + rand.Intn(6)
		} else {
			t.sats = 0
		}
		t.signal = 20 + rand.Intn(10)
		return
	}
	acc := t.StatusOn&1 != 0
	target := 0.0
	if acc && t.motion.Mode != "stop" {
		target = math.Max(0, t.motion.Speed+rand.Float64()*4-2)
	}
	// 加速 2 m/s²，减速 4 m/s²
	if t.speed < target {
		t.speed = math.Min(target, t.speed+7.2*dt)
	} else {
		t.speed = math.Max(target, t.speed-14.4*dt)
	}
	d := t.speed / 3.6 * dt
	switch t.motion.Mode {
	case "drive":
		t.dir = math.Mod(t.dir+rand.Float64()*6-3+360, 360)
	case "circle":
		if t.motion.center == [2]float64{} {
			t.motion.center = [2]float64{t.Lat, t.Lng}
		}
		// 半径 300m 绕圈：方向随角速度变化
		if t.speed > 0 {
			t.dir = math.Mod(t.dir+d/300*180/math.Pi, 360)
		}
	case "route":
		pts := t.routePoints()
		if len(pts) >= 2 {
			wp := pts[t.motion.wp%len(pts)]
			if distM(t.Lat, t.Lng, wp[0], wp[1]) < math.Max(d, 15) {
				t.motion.wp = (t.motion.wp + 1) % len(pts)
				wp = pts[t.motion.wp]
			}
			t.dir = bearing(t.Lat, t.Lng, wp[0], wp[1])
		}
	}
	gnssOK := t.alarmWord()&(1<<4|1<<5) == 0
	if d > 0 && gnssOK {
		t.Lat, t.Lng = moveBy(t.Lat, t.Lng, t.dir, d)
		t.track = append(t.track, [2]float64{t.Lng, t.Lat})
		if len(t.track) > 300 {
			t.track = t.track[len(t.track)-300:]
		}
	}
	if d > 0 {
		t.Mileage += d / 1000
		t.Fuel = math.Max(0, t.Fuel-d/1000*0.3) // 30L/100km
		t.markDirty()
	}
	if gnssOK {
		t.sats = 10 + rand.Intn(6)
	} else {
		t.sats = 0
	}
	t.signal = 20 + rand.Intn(10)
	t.alt = 20 + rand.Intn(3)
}

// routePoints 平台下发的第一条路线（拐点），否则默认路线
func (t *Terminal) routePoints() [][2]float64 {
	for _, a := range t.Areas {
		if a.Kind == 4 && len(a.Points) >= 2 {
			return a.Points
		}
	}
	return defaultRoute
}

// evalAlarms 按参数 0x0055~0x005C 判定。调用方持有 t.mu
func (t *Terminal) evalAlarms(dt int) {
	if !t.Cfg.AutoAlarms {
		for _, bit := range []int{1, 2, 13, 14, 15, 18, 19} {
			t.setAuto(bit, false)
		}
		return
	}
	day := time.Now().Format("20060102")
	if t.DayKey != day {
		t.DayKey, t.DayDrive = day, 0
	}
	p := t.Params
	moving := t.speed > 1
	if moving {
		t.contDrive += dt
		t.DayDrive += dt
		t.restSec, t.parkSec = 0, 0
	} else {
		t.restSec += dt
		t.parkSec += dt
		if t.restSec >= p.Int(0x0059) {
			t.contDrive = 0
		}
	}
	limit := float64(p.Int(0x0055))
	if limit > 0 && t.speed > limit {
		t.overSec += dt
	} else {
		t.overSec = 0
	}
	t.setAuto(1, limit > 0 && t.overSec >= p.Int(0x0056))
	t.setAuto(13, limit > 0 && t.speed > limit-float64(p.Int(0x005B))/10 && t.overSec < p.Int(0x0056))
	cont := p.Int(0x0057)
	t.setAuto(2, cont > 0 && t.contDrive >= cont)
	t.setAuto(14, cont > 0 && t.contDrive >= cont-p.Int(0x005C) && t.contDrive < cont)
	t.setAuto(18, p.Int(0x0058) > 0 && t.DayDrive >= p.Int(0x0058))
	t.setAuto(19, p.Int(0x005A) > 0 && t.parkSec >= p.Int(0x005A))
	// 违规行驶报警（2019 报警位 bit15）：在 0x0032 违规行驶时段内行驶。2013 无此报警位，只保存参数
	t.setAuto(15, t.Cfg.ver() >= V2019 && moving && inPeriod(p[0x0032], time.Now()))
}

// inPeriod 0x0032 违规行驶时段 BYTE[4]（hex）：开始时 分 结束时 分；开始=结束 视为未设置；可跨零点
func inPeriod(hexv string, now time.Time) bool {
	b := hexBytes(hexv, 4)
	start, end := int(b[0])*60+int(b[1]), int(b[2])*60+int(b[3])
	if start == end || b[0] > 23 || b[2] > 23 || b[1] > 59 || b[3] > 59 {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	if start < end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end
}
