package main

// 报文日志落盘与导出。
// 内存环（每个终端 logKeep=2000 条）只够看最近几分钟；需要按时间段导出时打开“落盘日志”：
// 所有终端（含系统日志）的日志按天写到 state/logs/YYYY-MM-DD.jsonl（单文件超过 32 MB 续写 YYYY-MM-DD-1.jsonl…），
// 总大小超过上限或超过保留天数时从最旧的文件开始删除。写盘走单独协程 + 缓冲通道，满了丢弃并计数，不阻塞收发。
// 取舍：默认开启（上限 200 MB、保留 7 天）——一个终端 10 秒一次 0200 约 3 MB/天，32 个终端也远低于上限；
// 压测虚拟终端不写报文日志，不会挤占空间。导出时有落盘日志用落盘日志，否则用内存环。

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	logChunkBytes  = 32 << 20  // 单个日志文件上限
	maxExportBytes = 200 << 20 // 单次导出上限
)

// LogSettings 落盘日志设置（state/logsettings.json）
type LogSettings struct {
	Enabled bool `json:"enabled"`
	MaxMB   int  `json:"maxMB"` // 总大小上限
	Days    int  `json:"days"`  // 保留天数
}

func (s *LogSettings) normalize() error {
	if s.MaxMB == 0 {
		s.MaxMB = 200
	}
	if s.Days == 0 {
		s.Days = 7
	}
	if s.MaxMB < 10 || s.MaxMB > 100000 {
		return fmt.Errorf("大小上限 10~100000 MB")
	}
	if s.Days < 1 || s.Days > 3650 {
		return fmt.Errorf("保留天数 1~3650")
	}
	return nil
}

// diskRec 落盘记录：日志项 + 来源终端
type diskRec struct {
	LogEntry
	Term  string `json:"term"`  // 终端编号；系统日志为空
	Phone string `json:"phone"` // 终端手机号
}

type logStore struct {
	mu      sync.Mutex
	set     LogSettings
	loaded  bool
	ch      chan diskRec
	flushCh chan chan struct{} // Flush 请求：由写协程处理，保证顺序
	f       *os.File
	w       *bufio.Writer
	day     string
	part    int
	size    int64
	dropped atomic.Int64
	lastErr string
	started bool
	active  bool // Start() 后才写盘（单元测试默认不写）
}

// Start 启用落盘（桌面版 / 命令行启动时调用）
func (s *logStore) Start() {
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
	go s.prune()
}

var logDisk = &logStore{}

func logDir() string          { return filepath.Join(stateDir, "logs") }
func logSettingsFile() string { return filepath.Join(stateDir, "logsettings.json") }

// Settings 读取设置（首次从磁盘加载；没有设置文件时默认开启）
func (s *logStore) Settings() LogSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.set
}

func (s *logStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.set = LogSettings{Enabled: true}
	readJSONDurable(logSettingsFile(), &s.set)
	s.set.normalize()
}

func (s *logStore) SetSettings(n LogSettings) error {
	if err := n.normalize(); err != nil {
		return err
	}
	s.mu.Lock()
	s.loadLocked()
	s.set = n
	if !n.Enabled {
		s.closeLocked()
	}
	b, _ := json.MarshalIndent(n, "", " ")
	s.mu.Unlock()
	if err := writeFileDurable(logSettingsFile(), b); err != nil {
		return err
	}
	s.prune()
	return nil
}

// write 由 logHub.add 调用：只入队，不做 IO
func (s *logStore) write(r diskRec) {
	s.mu.Lock()
	s.loadLocked()
	on := s.set.Enabled && s.active
	if on && !s.started {
		s.started = true
		s.ch, s.flushCh = make(chan diskRec, 8192), make(chan chan struct{})
		go s.loop()
	}
	ch := s.ch
	s.mu.Unlock()
	if !on {
		return
	}
	select {
	case ch <- r:
	default:
		s.dropped.Add(1)
	}
}

func (s *logStore) loop() {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	pruneTick := 0
	for {
		select {
		case r := <-s.ch:
			s.append(r)
		case done := <-s.flushCh:
			s.drain()
			close(done)
		case <-tk.C:
			s.drain()
			if pruneTick++; pruneTick%60 == 0 {
				s.prune()
			}
		}
	}
}

func (s *logStore) append(r diskRec) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set.Enabled {
		return
	}
	day := time.UnixMilli(r.T).Format("2006-01-02")
	if s.f == nil || day != s.day || s.size >= logChunkBytes {
		if err := s.openLocked(day); err != nil {
			s.lastErr = err.Error()
			return
		}
	}
	n, _ := s.w.Write(append(b, '\n'))
	s.size += int64(n)
}

// openLocked 打开当天（或续写分片）的文件
func (s *logStore) openLocked(day string) error {
	newDay := day != s.day
	s.closeLocked()
	if newDay {
		s.part = 0
		for { // 重启后续写当天最后一个未满的分片
			st, err := os.Stat(logPartPath(day, s.part+1))
			if err != nil || st == nil {
				break
			}
			s.part++
		}
	}
	os.MkdirAll(logDir(), 0o755)
	for {
		p := logPartPath(day, s.part)
		st, err := os.Stat(p)
		if err == nil && st.Size() >= logChunkBytes {
			s.part++
			continue
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		s.f, s.w, s.day, s.size = f, bufio.NewWriterSize(f, 64<<10), day, 0
		if st != nil {
			s.size = st.Size()
		}
		go s.prune()
		return nil
	}
}

func logPartPath(day string, part int) string {
	if part == 0 {
		return filepath.Join(logDir(), day+".jsonl")
	}
	return filepath.Join(logDir(), fmt.Sprintf("%s-%d.jsonl", day, part))
}

func (s *logStore) closeLocked() {
	if s.f != nil {
		s.w.Flush()
		s.f.Close()
		s.f, s.w = nil, nil
	}
}

// drain 写完队列并刷新缓冲（只在写协程中调用）
func (s *logStore) drain() {
	for {
		select {
		case r := <-s.ch:
			s.append(r)
			continue
		default:
		}
		break
	}
	s.mu.Lock()
	if s.w != nil {
		s.w.Flush()
	}
	s.mu.Unlock()
}

// Flush 把队列与缓冲写入磁盘（导出前、退出前调用）
func (s *logStore) Flush() {
	s.mu.Lock()
	fc := s.flushCh
	s.mu.Unlock()
	if fc == nil {
		return
	}
	done := make(chan struct{})
	select {
	case fc <- done:
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	case <-time.After(5 * time.Second):
	}
}

type logFile struct {
	Name string `json:"name"`
	Day  string `json:"day"`
	Size int64  `json:"size"`
	path string
}

func listLogFiles() []logFile {
	ents, _ := os.ReadDir(logDir())
	var out []logFile
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".jsonl") || len(n) < 16 {
			continue
		}
		if _, err := time.Parse("2006-01-02", n[:10]); err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, logFile{Name: n, Day: n[:10], Size: info.Size(), path: filepath.Join(logDir(), n)})
	}
	sort.Slice(out, func(i, j int) bool { // 按日期、分片号
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return partNo(out[i].Name) < partNo(out[j].Name)
	})
	return out
}

func partNo(name string) int {
	s := strings.TrimSuffix(name[10:], ".jsonl")
	n, _ := strconv.Atoi(strings.TrimPrefix(s, "-"))
	return n
}

// prune 删除超过保留天数或超出总大小的最旧文件（不删正在写的文件）
func (s *logStore) prune() {
	s.mu.Lock()
	set := s.set
	cur := ""
	if s.f != nil {
		cur = s.f.Name()
	}
	s.mu.Unlock()
	files := listLogFiles()
	var total int64
	for _, f := range files {
		total += f.Size
	}
	oldest := time.Now().AddDate(0, 0, -set.Days+1).Format("2006-01-02")
	limit := int64(set.MaxMB) << 20
	for _, f := range files {
		if f.path == cur {
			break
		}
		if f.Day < oldest || total > limit {
			if os.Remove(f.path) == nil {
				total -= f.Size
			}
		}
	}
}

// Status 设置 + 文件概况（页面显示）
func (s *logStore) Status() map[string]any {
	set := s.Settings()
	files := listLogFiles()
	var total int64
	for _, f := range files {
		total += f.Size
	}
	v := map[string]any{"settings": set, "dir": logDir(), "files": len(files), "totalBytes": total, "dropped": s.dropped.Load(),
		"ringKeep": logKeep}
	if len(files) > 0 {
		v["oldest"], v["newest"] = files[0].Day, files[len(files)-1].Day
	}
	s.mu.Lock()
	v["lastError"] = s.lastErr
	s.mu.Unlock()
	return v
}

// ---------------- 导出 ----------------

type LogFilter struct {
	Terms  map[string]bool // 终端编号；空 = 全部；"sys" = 系统日志
	From   int64           // 毫秒（含）
	To     int64           // 毫秒（含）；0 = 不限
	Dirs   map[string]bool // up/down/sys/err；空 = 全部
	MsgIDs map[string]bool // 4 位大写 hex；空 = 全部
	Text   string          // 摘要包含
}

func splitSet(s string, f func(string) string) map[string]bool {
	m := map[string]bool{}
	for _, x := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '，' || r == ';' }) {
		if x = f(strings.TrimSpace(x)); x != "" {
			m[x] = true
		}
	}
	return m
}

// parseLogFilter 查询参数：term=1,2|all  from/to=毫秒或 2006-01-02T15:04[:05]  dir=up,down  msgId=0200,0x8103  q=文本
func parseLogFilter(q map[string][]string) (LogFilter, error) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	f := LogFilter{Text: get("q")}
	if t := get("term"); t != "" && t != "all" {
		f.Terms = splitSet(t, func(s string) string { return s })
	}
	f.Dirs = splitSet(get("dir"), strings.ToLower)
	for d := range f.Dirs {
		if !oneOf(d, "up", "down", "sys", "err") {
			return f, fmt.Errorf("方向只能是 up/down/sys/err")
		}
	}
	var bad error
	f.MsgIDs = splitSet(get("msgId"), func(s string) string {
		s = strings.TrimPrefix(strings.ToLower(s), "0x")
		n, err := strconv.ParseUint(s, 16, 16)
		if err != nil {
			bad = fmt.Errorf("消息 ID %q 无效", s)
			return ""
		}
		return fmt.Sprintf("%04X", n)
	})
	if bad != nil {
		return f, bad
	}
	for _, k := range []string{"from", "to"} {
		v := get(k)
		if v == "" {
			continue
		}
		ms, ok := parseExportTime(v)
		if !ok {
			return f, fmt.Errorf("时间 %s=%q 无效", k, v)
		}
		if k == "from" {
			f.From = ms
		} else {
			f.To = ms
		}
	}
	if f.To > 0 && f.From > f.To {
		return f, fmt.Errorf("开始时间晚于结束时间")
	}
	return f, nil
}

func parseExportTime(v string) (int64, bool) {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 1e11 {
		return n, true
	}
	for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(l, v, time.Local); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

func (f *LogFilter) match(r *diskRec) bool {
	term := r.Term
	if term == "" {
		term = "sys"
	}
	return (len(f.Terms) == 0 || f.Terms[term]) && r.T >= f.From && (f.To == 0 || r.T <= f.To) &&
		(len(f.Dirs) == 0 || f.Dirs[r.Dir]) && (len(f.MsgIDs) == 0 || f.MsgIDs[r.MsgID]) &&
		(f.Text == "" || strings.Contains(r.Summary, f.Text) || strings.Contains(r.Name, f.Text))
}

// collectLogs 按过滤条件取日志（时间升序）；source=disk/memory/空（自动：有落盘文件用落盘，否则内存环）
func collectLogs(m *Manager, f LogFilter, source string, each func(*diskRec) error) (string, error) {
	if source == "" {
		source = "memory"
		if logDisk.Settings().Enabled && len(listLogFiles()) > 0 {
			source = "disk"
		}
	}
	switch source {
	case "disk":
		logDisk.Flush()
		fromDay, toDay := "", ""
		if f.From > 0 {
			fromDay = time.UnixMilli(f.From).Format("2006-01-02")
		}
		if f.To > 0 {
			toDay = time.UnixMilli(f.To).Format("2006-01-02")
		}
		for _, lf := range listLogFiles() {
			if fromDay != "" && lf.Day < fromDay || toDay != "" && lf.Day > toDay {
				continue
			}
			if err := scanLogFile(lf.path, f, each); err != nil {
				return source, err
			}
		}
	case "memory":
		var all []diskRec
		add := func(h *logHub, id, phone string) {
			_, hist := h.snapshot()
			for _, e := range hist {
				r := diskRec{LogEntry: e, Term: id, Phone: phone}
				if f.match(&r) {
					all = append(all, r)
				}
			}
		}
		add(hub, "", "")
		for _, t := range m.List() {
			add(t.logHub(), t.ID, t.phoneNow())
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].T < all[j].T })
		for i := range all {
			if err := each(&all[i]); err != nil {
				return source, err
			}
		}
	default:
		return source, fmt.Errorf("source 只能是 disk/memory")
	}
	return source, nil
}

func scanLogFile(path string, f LogFilter, each func(*diskRec) error) error {
	fh, err := os.Open(path)
	if err != nil {
		return nil // 刚被清理
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var r diskRec
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue // 写到一半的行
		}
		if f.match(&r) {
			if err := each(&r); err != nil {
				return err
			}
		}
	}
	return nil
}

var dirNames = map[string]string{"up": "上行", "down": "下行", "sys": "系统", "err": "错误"}

var errExportTooBig = fmt.Errorf("导出内容超过 %d MB，请缩小时间范围或筛选条件", maxExportBytes>>20)

// exportLogs 生成 CSV（带 UTF-8 BOM，Excel 可直接打开）或原始 hex 文本（每行一帧，只含上下行报文）
func exportLogs(m *Manager, f LogFilter, format, source string) ([]byte, int, string, error) {
	var buf bytes.Buffer
	n := 0
	var cw *csv.Writer
	switch format {
	case "", "csv":
		buf.WriteString("\ufeff")
		cw = csv.NewWriter(&buf)
		cw.Write([]string{"时间", "终端", "手机号", "方向", "消息ID", "名称", "流水号", "长度", "摘要", "hex"})
	case "hex":
		fmt.Fprintf(&buf, "# JT808 模拟器报文导出 %s；每行一帧（转义后的完整帧 hex），以 # 开头的行为注释\n", time.Now().Format("2006-01-02 15:04:05"))
	default:
		return nil, 0, "", fmt.Errorf("format 只能是 csv/hex")
	}
	src, err := collectLogs(m, f, source, func(r *diskRec) error {
		ts := time.UnixMilli(r.T).Format("2006-01-02 15:04:05.000")
		if cw != nil {
			term := ""
			if r.Term != "" {
				term = "#" + r.Term
			}
			serial, ln := "", ""
			if r.Dir == "up" || r.Dir == "down" {
				serial, ln = itoa(r.Serial), itoa(r.Len)
			}
			cw.Write([]string{ts, term, r.Phone, firstNonEmpty(dirNames[r.Dir], r.Dir), r.MsgID, r.Name, serial, ln, r.Summary, r.Hex})
		} else {
			if r.Hex == "" || r.Dir != "up" && r.Dir != "down" {
				return nil
			}
			fmt.Fprintf(&buf, "# %s #%s %s %s %s %s\n%s\n", ts, r.Term, r.Phone, map[string]string{"up": ">>", "down": "<<"}[r.Dir], r.MsgID, r.Name, r.Hex)
		}
		n++
		if buf.Len() > maxExportBytes {
			return errExportTooBig
		}
		return nil
	})
	if cw != nil {
		cw.Flush()
	}
	return buf.Bytes(), n, src, err
}

func registerLogAPI(mux *http.ServeMux, m *Manager) {
	mux.HandleFunc("GET /api/logs/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, logDisk.Status())
	})
	mux.HandleFunc("POST /api/logs/settings", func(w http.ResponseWriter, r *http.Request) {
		var s LogSettings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&s); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "JSON 无效"})
			return
		}
		if err := logDisk.SetSettings(s); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "status": logDisk.Status()})
	})
	// GET /api/logs/export?term=&from=&to=&dir=&msgId=&q=&format=csv|hex&source=disk|memory[&save=1]
	mux.HandleFunc("GET /api/logs/export", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, err := parseLogFilter(q)
		if err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		format := q.Get("format")
		data, n, src, err := exportLogs(m, f, format, q.Get("source"))
		if err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		ext, ct := ".csv", "text/csv; charset=utf-8"
		if format == "hex" {
			ext, ct = ".txt", "text/plain; charset=utf-8"
		}
		who := "all"
		if len(f.Terms) == 1 {
			for k := range f.Terms {
				who = "t" + k
			}
		}
		w.Header().Set("X-JT808-Rows", itoa(n))
		w.Header().Set("X-JT808-Source", src)
		serveDownload(w, r, fmt.Sprintf("jt808-log-%s-%s%s", who, time.Now().Format("20060102-150405"), ext), ct, data)
	})
}
