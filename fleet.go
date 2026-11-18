package main

// 多终端总览：一次返回全部终端的在线状态、位置/速度、报警、最近一条报文（页面轮询 GET /api/fleet）

import (
	"net/http"
	"sort"
)

type FleetLast struct {
	T     int64  `json:"t"`
	Dir   string `json:"dir"`
	MsgID string `json:"msgId"`
	Name  string `json:"name"`
}

type FleetItem struct {
	ID          string     `json:"id"`
	Phone       string     `json:"phone"`
	Plate       string     `json:"plate"`
	Status      string     `json:"status"`
	OnlineSince int64      `json:"onlineSince"`
	Host        string     `json:"host"`
	Port        int        `json:"port"`
	Version     string     `json:"version"`
	Transport   string     `json:"transport"`
	Lng         float64    `json:"lng"`
	Lat         float64    `json:"lat"`
	Speed       float64    `json:"speed"`
	Dir         int        `json:"dir"`
	Mode        string     `json:"mode"`
	Alarms      []string   `json:"alarms"`
	Up          int        `json:"up"`
	Down        int        `json:"down"`
	Last        *FleetLast `json:"last,omitempty"`
	LastUp      int64      `json:"lastUp"` // 最近一次上行时间（毫秒）
	Scenario    string     `json:"scenario,omitempty"`
	Default     bool       `json:"default"`
	LastError   string     `json:"lastError,omitempty"`
}

// lastMsg 内存环中最近一条上/下行报文
func (h *logHub) lastMsg() *FleetLast {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.ring) - 1; i >= 0; i-- {
		if e := h.ring[i]; e.Dir == "up" || e.Dir == "down" {
			return &FleetLast{T: e.T, Dir: e.Dir, MsgID: e.MsgID, Name: e.Name}
		}
	}
	return nil
}

func (m *Manager) Fleet() []FleetItem {
	def := m.Default()
	running := map[string]string{}
	for _, v := range scenarios.Views(m) {
		if v.Status == "running" {
			running[v.Terminal] = v.Name
		}
	}
	out := []FleetItem{}
	for _, t := range m.List() {
		t.mu.Lock()
		c := t.Cfg
		it := FleetItem{ID: t.ID, Phone: c.Phone, Plate: c.Plate, Status: t.status, Host: c.Host, Port: c.Port, Version: c.Version,
			Transport: c.transport(), Lng: t.Lng, Lat: t.Lat, Speed: t.speed, Dir: int(t.dir), Mode: t.motion.Mode, Alarms: []string{},
			Up: t.counters.Up, Down: t.counters.Down, Default: t.ID == def, Scenario: running[t.ID]}
		if t.status == "online" {
			it.OnlineSince = t.online.UnixMilli()
		} else {
			it.LastError = t.lastErr
		}
		if !t.lastUp.IsZero() {
			it.LastUp = t.lastUp.UnixMilli()
		}
		for _, b := range bits(alarmNames, t.alarmWord()) {
			if b.On {
				it.Alarms = append(it.Alarms, b.Name)
			}
		}
		for _, b := range bits(videoAlarmNames, t.videoWord()) {
			if b.On {
				it.Alarms = append(it.Alarms, b.Name)
			}
		}
		t.mu.Unlock()
		it.Last = t.logHub().lastMsg()
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return compareTermID(out[i].ID, out[j].ID)
	})
	return out
}

func registerFleetAPI(mux *http.ServeMux, m *Manager) {
	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		list := m.Fleet()
		on, alarm := 0, 0
		for _, it := range list {
			if it.Status == "online" {
				on++
			}
			if len(it.Alarms) > 0 {
				alarm++
			}
		}
		writeJSON(w, 200, map[string]any{"terminals": list, "total": len(list), "online": on, "alarming": alarm, "max": maxTerminals})
	})
}
