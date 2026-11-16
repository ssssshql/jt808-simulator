package main

// 位置汇报策略：0x0020 定时/定距，0x0021 按 ACC / 登录+ACC，紧急报警间隔，拐点补传，8202 临时跟踪；离线时进盲区

import (
	"fmt"
	"time"
)

// reportRule 当前汇报间隔(秒)与距离(米)，0 表示不按该维度汇报。调用方持有 t.mu
func (t *Terminal) reportRule() (sec, meters int, why string) {
	p := t.Params
	if time.Now().Before(t.tempUntil) {
		return t.tempInt, 0, "临时跟踪"
	}
	strategy := p.Int(0x0020)
	pick := func(s, d int) (int, int) {
		switch strategy {
		case 1:
			return 0, d
		case 2:
			return s, d
		}
		return s, 0
	}
	if t.alarmWord()&1 != 0 {
		s, d := pick(p.Int(0x0028), p.Int(0x002F))
		return s, d, "紧急报警"
	}
	if p.Int(0x0021) == 1 && !t.Driver.OnDuty {
		s, d := pick(p.Int(0x0022), p.Int(0x002D))
		return s, d, "驾驶员未登录"
	}
	if t.StatusOn&1 == 0 {
		s, d := pick(p.Int(0x0027), p.Int(0x002E))
		return s, d, "休眠(ACC 关)"
	}
	s, d := pick(p.Int(0x0029), p.Int(0x002C))
	return s, d, "缺省"
}

// reportTick 每秒判断是否需要汇报
func (t *Terminal) reportTick() {
	t.mu.Lock()
	if !t.want || t.powerOff {
		t.mu.Unlock()
		return
	}
	sec, meters, _ := t.reportRule()
	now := time.Now()
	lr := t.lastRpt
	due := t.forceRpt
	if lim := now.Add(time.Duration(sec) * time.Second); sec > 0 && t.nextRpt.After(lim) {
		t.nextRpt = lim // 汇报间隔被改小（8103/页面）后立即按新间隔执行
	}
	if sec > 0 && !now.Before(t.nextRpt) {
		due = true
	}
	if meters > 0 && distM(lr.lat, lr.lng, t.Lat, t.Lng) >= float64(meters) {
		due = true
	}
	// 状态/报警变化立即汇报
	if t.statusWord() != lr.status || t.alarmWord() != lr.alarm {
		due = true
	}
	// 拐点补传
	if a := t.Params.Int(0x0030); a > 0 && t.speed > 5 && angleDiff(t.dir, lr.dir) >= float64(a) {
		due = true
	}
	if !due {
		t.mu.Unlock()
		return
	}
	t.alarmPhotos(lr.alarm, t.alarmWord())
	body := t.locBody(t.extra)
	t.extra, t.forceRpt = nil, false
	t.lastRpt.lng, t.lastRpt.lat, t.lastRpt.dir = t.Lng, t.Lat, t.dir
	t.lastRpt.status, t.lastRpt.alarm = t.statusWord(), t.alarmWord()
	if sec > 0 {
		t.nextRpt = now.Add(time.Duration(sec) * time.Second)
	} else {
		t.nextRpt = now.Add(time.Hour)
	}
	online := t.status == "online"
	summary := t.locSummary()
	if a := t.alarmWord(); a != 0 {
		summary += fmt.Sprintf(" 报警=%08X", a)
	}
	t.mu.Unlock()
	if !online {
		t.storeBlind(body)
		return
	}
	if _, _, err := t.send(0x0200, body, summary); err != nil {
		t.storeBlind(body)
	}
}

// reportNow 立即汇报（页面"立即上报"）
func (t *Terminal) reportNow() {
	t.mu.Lock()
	t.forceRpt = true
	t.mu.Unlock()
	t.reportTick()
}

// photoTick 定时拍照 0x0064 / 定距拍照 0x0065：bit0-4 通道1-5 开关，bit8-12 存储标志（0 存储 1 上传），bit16 单位，bit17-31 间隔
func (t *Terminal) photoTick() {
	t.mu.Lock()
	if t.status != "online" {
		t.mu.Unlock()
		return
	}
	var shots []int
	var store []bool
	var events []int
	for _, k := range []int{0x0064, 0x0065} {
		v := t.Params.Int(k)
		n := v >> 17
		if v&0x1F == 0 || n == 0 {
			continue
		}
		if k == 0x0064 {
			step := n
			if v&(1<<16) != 0 {
				step = n * 60
			}
			if time.Since(t.lastPhoto[k]) < time.Duration(step)*time.Second {
				continue
			}
		} else {
			step := float64(n)
			if v&(1<<16) != 0 {
				step *= 1000
			}
			if (t.Mileage-t.lastPhotoKm)*1000 < step {
				continue
			}
			t.lastPhotoKm = t.Mileage
		}
		t.lastPhoto[k] = time.Now()
		for ch := 0; ch < 5; ch++ {
			if v&(1<<ch) != 0 {
				shots = append(shots, ch+1)
				store = append(store, v&(1<<(8+ch)) == 0)                     // 表 14/15：存储标志 0 存储 1 上传
				events = append(events, map[int]int{0x0064: 1, 0x0065: 4}[k]) // 1 定时动作 4 定距动作
			}
		}
	}
	t.mu.Unlock()
	for i, ch := range shots {
		go t.capture(ch, events[i], store[i], 0)
	}
}
