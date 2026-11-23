package main

// Web 界面：GET / 静态页，GET /api/state，POST /api/action，GET /api/log (SSE)

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

//go:embed all:web
var webFS embed.FS

type ConnView struct {
	Status        string `json:"status"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Phone         string `json:"phone"`
	Version       string `json:"version"`
	Transport     string `json:"transport"`
	Token         string `json:"token"`
	OnlineSince   int64  `json:"onlineSince"`
	Reconnects    int    `json:"reconnects"`
	AutoReconnect bool   `json:"autoReconnect"`
	LastError     string `json:"lastError"`
}

func (t *Terminal) State() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.Cfg
	since := int64(0)
	if t.status == "online" {
		since = t.online.UnixMilli()
	}
	sec, _, why := t.reportRule()
	next := max(0, int(time.Until(t.nextRpt).Seconds()))
	if sec == 0 {
		next = -1
	}
	temp := ""
	if time.Now().Before(t.tempUntil) {
		temp = fmt.Sprintf("每 %ds，剩余 %ds", t.tempInt, int(time.Until(t.tempUntil).Seconds()))
	}
	track := make([]map[string]float64, 0, len(t.track))
	for _, p := range t.track {
		track = append(track, map[string]float64{"lng": p[0], "lat": p[1]})
	}
	media := make([]map[string]any, 0, len(t.Media))
	for i := len(t.Media) - 1; i >= 0; i-- {
		m := t.Media[i]
		media = append(media, map[string]any{"id": m.ID, "type": mediaTypeName[m.Type], "channel": m.Channel,
			"event": mediaEventName[m.Event], "t": m.T, "size": m.Size})
	}
	streams := []avStream{}
	for _, s := range t.streams {
		streams = append(streams, avStream{Channel: s.Channel, Kind: s.Kind, Target: s.Target, Frames: s.Frames})
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].Channel < streams[j].Channel })
	inbox := make([]InboxItem, 0, len(t.Inbox))
	for i := len(t.Inbox) - 1; i >= 0; i-- {
		inbox = append(inbox, t.Inbox[i])
	}
	return map[string]any{
		"id":   t.ID,
		"conn": ConnView{t.status, c.Host, c.Port, c.Phone, c.Version, c.transport(), c.Token, since, t.reconnects, c.AutoReconnect, t.lastErr},
		"device": map[string]any{"plate": c.Plate, "plateColor": c.PlateColor, "deviceId": c.DeviceID, "model": c.Model,
			"maker": c.Maker, "iccid": c.ICCID, "fwVer": c.FwVer},
		"config": c,
		"loc": map[string]any{"lng": t.Lng, "lat": t.Lat, "alt": t.alt, "speed": t.speed, "dir": int(t.dir),
			"mileage": t.Mileage, "fuel": t.Fuel, "sats": t.sats, "signal": t.signal, "tires": t.Tires, "cabinTemp": t.CabinTemp},
		"track":       track,
		"status":      t.statusBits(),
		"alarms":      t.alarmBits(),
		"videoAlarms": bits(videoAlarmNames, t.videoWord()),
		"motion": map[string]any{"mode": t.motion.Mode, "speed": t.motion.Speed, "reportInterval": sec,
			"reportRule": why, "nextReportIn": next, "tempTrack": temp},
		"reply": map[string]any{"mode": t.replyMode, "delay": t.replyDelay},
		"counters": map[string]int{"up": t.counters.Up, "down": t.counters.Down, "pendingAcks": len(t.pending),
			"retrans": t.counters.Retrans, "blindQueue": len(t.Blind), "blindCap": c.blindCap(), "blindDropped": t.blindDropped},
		"inbox":      inbox,
		"media":      media,
		"params":     t.paramViews(),
		"streams":    streams,
		"driver":     t.Driver,
		"areas":      append([]Area{}, t.Areas...),
		"signals":    bits(extSignalNames, t.ExtSignal),
		"io":         bits(ioNames, uint32(t.IOStatus)),
		"analog":     map[string]int{"ad0": t.Analog[0], "ad1": t.Analog[1]},
		"rec":        t.Rec,
		"rsa":        map[string]any{"platKey": t.PlatRSA.pub() != nil, "upEncrypt": c.UpRSA},
		"replay":     t.replayView(),
		"safety":     t.safetyView(),
		"mediaSrc":   t.mediaView(),
		"mediaStamp": !c.NoStamp, // 附件画面是否烧入时间水印（见 stamp.go）
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// termHandler 从路径 {id} 取终端（旧接口无 {id} 时为默认终端）
type termHandler func(w http.ResponseWriter, r *http.Request, t *Terminal)

func newWebMux(m *Manager) http.Handler {
	mux := http.NewServeMux()
	web, build := webHandler()
	mux.Handle("GET /", web)
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		v := buildInfo()
		v.Web = build
		writeJSON(w, 200, v)
	})
	registerUpdateAPI(mux)
	registerScenarioAPI(mux, m)
	registerDownloadAPI(mux)
	registerLogAPI(mux, m)
	registerFleetAPI(mux, m)
	registerStressReportAPI(mux)
	// 终端级接口：/api/t/{id}/xxx；旧接口 /api/xxx 作用于默认终端（兼容脚本与旧页面）
	handle := func(pattern string, h termHandler) {
		method, path, _ := strings.Cut(pattern, " ")
		f := func(w http.ResponseWriter, r *http.Request) {
			t := m.Get(r.PathValue("id"))
			if t == nil {
				writeJSON(w, 404, map[string]any{"ok": false, "error": "终端不存在: " + r.PathValue("id")})
				return
			}
			h(w, r, t)
		}
		mux.HandleFunc(method+" /api"+path, f)
		mux.HandleFunc(method+" /api/t/{id}"+path, f)
	}
	handle("GET /state", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		w.Header().Set("X-JT808-Build", build)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, t.State())
	})
	handle("GET /state/stream", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		serveStateStream(w, r, m, t, build)
	})
	handle("GET /params", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		t.mu.Lock()
		list, ver := t.paramFull(), t.Cfg.Version
		t.mu.Unlock()
		writeJSON(w, 200, map[string]any{"groups": paramGroups, "params": list, "version": ver})
	})
	handle("GET /profile", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		p := t.exportProfile()
		safePhone := strings.Map(func(c rune) rune {
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' || c == '_' {
				return c
			}
			return -1
		}, p.Cfg.Phone)
		if safePhone == "" {
			safePhone = "terminal"
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="jt808sim-%s.json"`, safePhone))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetIndent("", " ")
		enc.Encode(p)
	})
	handle("POST /profile", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		var p Profile
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&p); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "JSON 无效: " + err.Error()})
			return
		}
		if err := t.importProfile(p); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		m.save()
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	handle("POST /action", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		var p map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "JSON 无效"})
			return
		}
		typ, _ := p["type"].(string)
		err := t.Do(typ, p)
		t.kick.notify() // 状态推送立即刷新
		if typ == "setConfig" {
			m.save() // 手机号可能变化
		}
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	handle("GET /route", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, t.routeView())
	})
	// 导入轨迹：请求体为 GPX / CSV 文件内容，?name=名称&format=gpx|csv（缺省自动识别）
	handle("POST /route", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRouteBytes))
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "读取失败: " + err.Error()})
			return
		}
		v, err := t.importRoute(r.URL.Query().Get("name"), r.URL.Query().Get("format"), data)
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "replay": v})
	})
	handle("GET /log", func(w http.ResponseWriter, r *http.Request, t *Terminal) { serveLog(w, r, t.logHub()) })
	registerMediaSrcAPI(handle)
	registerIntercomAPI(handle)
	mux.HandleFunc("GET /api/syslog", func(w http.ResponseWriter, r *http.Request) { serveLog(w, r, hub) })

	// 压力测试（进程级）
	mux.HandleFunc("GET /api/stress", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, stress.View())
	})
	mux.HandleFunc("POST /api/stress", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Op string `json:"op"`
			StressConfig
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "JSON 无效: " + err.Error()})
			return
		}
		var err error
		switch p.Op {
		case "start":
			err = stress.Start(p.StressConfig)
		case "stop":
			stress.Stop("手动停止")
		default:
			err = fmt.Errorf("未知操作 %s（start/stop）", p.Op)
		}
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// 终端管理
	mux.HandleFunc("GET /api/terminals", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{"default": m.Default(), "max": maxTerminals, "terminals": m.Briefs()})
	})
	mux.HandleFunc("POST /api/terminals", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "JSON 无效"})
			return
		}
		op, id := pstr(p, "op", ""), pstr(p, "id", "")
		var err error
		res := map[string]any{"ok": true}
		switch op {
		case "add", "clone":
			base := m.Get(id)
			if base == nil {
				err = fmt.Errorf("终端 #%s 不存在", id)
				break
			}
			var t *Terminal
			if t, err = m.Add(base, op == "clone"); err == nil {
				res["id"] = t.ID
			}
		case "delete":
			err = m.Delete(id, pint(p, "purge", 0) == 1)
		case "default":
			err = m.SetDefault(id)
		default:
			err = fmt.Errorf("未知操作 %s（add/clone/delete/default）", op)
		}
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, res)
	})
	return withVersion(mux)
}

// serveLog SSE 推送某个 hub 的日志（先补发最近 500 条）
func serveLog(w http.ResponseWriter, r *http.Request, h *logHub) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	ch, hist := h.sub()
	defer h.unsub(ch)
	if len(hist) > 500 {
		hist = hist[len(hist)-500:]
	}
	for _, e := range hist {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}

// withVersion 所有响应带 X-JT808-Version（程序版本），与 X-JT808-Build（前端资源哈希）并列
func withVersion(h http.Handler) http.Handler {
	v := buildInfo().Version
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-JT808-Version", v)
		h.ServeHTTP(w, r)
	})
}

// newToken 桌面版本地接口的随机令牌（128 位）；rand 不可用时返回空串（调用方拒绝启动）
func newToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// webGuard 桌面版内置服务的防护（始终启用；token 为空时拒绝全部请求，不会退化为不设防）：
//   - Host 必须是回环地址，防 DNS rebinding（恶意域名解析到 127.0.0.1 后带外源 Host 直接访问）；
//   - 请求须带启动令牌：首次 ?k=<token> 校验通过后写 HttpOnly cookie，之后同源页面（含 SSE）自动携带。
//
// 这样本机其它进程和网页（即便猜到随机端口）都无法读写终端状态或触发操作。
func webGuard(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = strings.Trim(r.Host, "[]")
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if c, err := r.Cookie("jt808k"); err == nil &&
			subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("k")), []byte(token)) == 1 {
			http.SetCookie(w, &http.Cookie{Name: "jt808k", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}
