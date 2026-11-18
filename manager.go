package main

// 多终端：一个进程内同时运行多个模拟终端，各自独立的配置、连接、运动、参数与状态。
// 终端清单保存在 state/terminals.json（编号 -> 手机号），每个终端的完整状态仍按手机号保存在 state/<手机号>.json，
// 因此旧版本的单终端状态无需迁移：首次启动时自动成为 1 号终端。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxTerminals = 32

type termRec struct {
	ID    string `json:"id"`
	Phone string `json:"phone"`
}

type registry struct {
	Default   string    `json:"default"`
	NextID    int       `json:"nextId"`
	Terminals []termRec `json:"terminals"`
}

type Manager struct {
	mu    sync.Mutex
	addMu sync.Mutex // 串行化新增/复制/删除，避免并发挑到同一个手机号
	terms []*Terminal
	def   string
	next  int
	saved []byte
}

func registryFile() string { return filepath.Join(stateDir, "terminals.json") }

// NewManager 读取终端清单；没有清单时以命令行配置创建 1 号终端（加载 state/<phone>.json，与旧版本一致）。
// 命令行显式指定的值覆盖到默认终端；-phone 与清单中某个终端一致时覆盖到该终端。
func NewManager(flags Config) *Manager {
	m := &Manager{next: 1}
	var reg registry
	if b, err := os.ReadFile(registryFile()); err == nil {
		if err := json.Unmarshal(b, &reg); err != nil {
			errf("读取 %s 失败: %v（按单终端启动）", registryFile(), err)
			reg = registry{}
		}
	}
	seen := map[string]bool{}
	for _, r := range reg.Terminals {
		if r.ID == "" || seen[r.ID] || len(m.terms) >= maxTerminals {
			continue
		}
		seen[r.ID] = true
		m.terms = append(m.terms, m.spawn(r.ID, Config{Phone: r.Phone}))
		if n, err := strconv.Atoi(r.ID); err == nil && n >= m.next {
			m.next = n + 1
		}
	}
	if reg.NextID > m.next {
		m.next = reg.NextID
	}
	if len(m.terms) == 0 {
		m.terms = []*Terminal{m.spawn(m.allocID(), flags)}
	} else {
		m.def = reg.Default
		target := m.Get("")
		if flags.Phone != "" {
			for _, t := range m.terms {
				if t.phoneNow() == flags.Phone {
					target = t
				}
			}
		}
		target.applyFlags(flags)
	}
	if m.Get(m.def) == nil || m.def == "" {
		m.def = m.terms[0].ID
	}
	m.save()
	return m
}

func (m *Manager) allocID() string {
	id := strconv.Itoa(m.next)
	m.next++
	return id
}

// spawn 创建终端并启动其运动/保存循环
func (m *Manager) spawn(id string, cfg Config) *Terminal {
	t := NewTerminal(cfg)
	t.ID, t.mgr = id, m
	t.hub.prefix, t.hub.owner = "#"+id, t
	go t.motionLoop()
	go t.saveLoop()
	return t
}

// applyFlags 命令行参数覆盖（-phone 不同则先切换到该手机号保存的状态）
func (t *Terminal) applyFlags(cfg Config) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cfg.Phone != "" && cfg.Phone != t.Cfg.Phone {
		if t.Cfg.Phone != "" {
			t.save()
		}
		t.Cfg.Phone = cfg.Phone
		t.load()
	}
	t.Cfg.overlay(cfg)
	t.fillDefaults()
	t.phone.Store(t.Cfg.Phone)
	t.markDirty()
}

func (t *Terminal) phoneNow() string {
	s, _ := t.phone.Load().(string)
	return s
}

// List 终端快照（按编号排列）
func (m *Manager) List() []*Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Terminal(nil), m.terms...)
}

// Get 按编号取终端；id 为空返回默认终端；不存在返回 nil
func (m *Manager) Get(id string) *Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" {
		id = m.def
	}
	for _, t := range m.terms {
		if t.ID == id {
			return t
		}
	}
	if id == m.def && len(m.terms) > 0 {
		return m.terms[0]
	}
	return nil
}

func (m *Manager) Default() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.def
}

// phoneUsed 手机号是否已被其它终端使用（只读各终端的手机号副本，不取终端锁，可在持有 t.mu 时调用）
func (m *Manager) phoneUsed(phone string, except *Terminal) bool {
	if m == nil || phone == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.terms {
		if t != except && t.phoneNow() == phone {
			return true
		}
	}
	return false
}

// freePhone 从 base 起递增，找一个未被终端使用、也没有状态文件的手机号（保持位数）
func (m *Manager) freePhone(base string) string {
	if base == "" || strings.Trim(base, "0123456789") != "" {
		base = "13800000001"
	}
	n, err := strconv.ParseUint(base, 10, 64)
	if err != nil {
		n = 13800000001
	}
	width := len(base)
	for i := 1; i < 100000; i++ {
		p := fmt.Sprintf("%0*d", width, n+uint64(i))
		if len(p) > 20 {
			break
		}
		if m.phoneUsed(p, nil) {
			continue
		}
		if _, err := os.Stat(filepath.Join(stateDir, p+".json")); err == nil {
			continue
		}
		return p
	}
	return fmt.Sprintf("1%010d", time.Now().UnixNano()%1e10)
}

var tailNum = regexp.MustCompile(`\d+$`)

// incTail 末尾数字加 n（保持位数），如 粤A12345 -> 粤A12346；没有末尾数字时原样返回
func incTail(s string, n int) string {
	loc := tailNum.FindStringIndex(s)
	if loc == nil {
		return s
	}
	d := s[loc[0]:]
	v, err := strconv.ParseUint(d, 10, 64)
	if err != nil {
		return s
	}
	r := strconv.FormatUint(v+uint64(n), 10)
	if len(r) > len(d) {
		r = r[len(r)-len(d):]
	}
	return s[:loc[0]] + strings.Repeat("0", len(d)-len(r)) + r
}

// Add 新增终端：clone=true 时复制 base 的配置/参数/区域/位置等（手机号、车牌、IMEI 末位递增，清除鉴权码）；
// 否则用默认配置，只沿用 base 的网关地址与协议版本
func (m *Manager) Add(base *Terminal, clone bool) (*Terminal, error) {
	m.addMu.Lock()
	defer m.addMu.Unlock()
	if len(m.List()) >= maxTerminals {
		return nil, fmt.Errorf("最多 %d 个终端（批量压测请用“压力测试”）", maxTerminals)
	}
	var prof Profile
	basePhone := ""
	if base != nil {
		prof = base.exportProfile()
		basePhone = prof.Cfg.Phone
	}
	phone := m.freePhone(basePhone)
	m.mu.Lock()
	id := m.allocID()
	m.mu.Unlock()
	var t *Terminal
	if clone && base != nil {
		step := 1
		if a, e1 := strconv.ParseUint(phone, 10, 64); e1 == nil {
			if b, e2 := strconv.ParseUint(basePhone, 10, 64); e2 == nil && a > b && a-b < 1000 {
				step = int(a - b)
			}
		}
		c := prof.Cfg
		derived := c.DeviceID == "" || strings.HasSuffix(basePhone, c.DeviceID)
		c.Phone, c.Token = phone, ""
		c.Plate, c.IMEI, c.VIN = incTail(c.Plate, step), incTail(c.IMEI, step), incTail(c.VIN, step)
		if derived {
			c.DeviceID = ""
		} else {
			c.DeviceID = incTail(c.DeviceID, step)
		}
		prof.Cfg = c
		if prof.Params != nil {
			delete(prof.Params, 0x0083) // 车牌以 cfg 为准
		}
		t = NewTerminal(Config{Phone: phone})
		if err := t.importProfile(prof); err != nil {
			return nil, err
		}
	} else {
		cfg := Config{Phone: phone}
		if base != nil {
			cfg.Host, cfg.Port, cfg.Version, cfg.Transport = prof.Cfg.Host, prof.Cfg.Port, prof.Cfg.Version, prof.Cfg.Transport
		}
		t = NewTerminal(cfg)
	}
	t.ID, t.mgr = id, m
	t.hub.prefix, t.hub.owner = "#"+id, t
	t.mu.Lock()
	t.markDirty()
	t.save()
	t.mu.Unlock()
	go t.motionLoop()
	go t.saveLoop()
	m.mu.Lock()
	m.terms = append(m.terms, t)
	m.mu.Unlock()
	m.save()
	what := "新增"
	if clone {
		what = "复制"
	}
	t.logf("%s终端 #%s phone=%s", what, id, phone)
	return t, nil
}

// Delete 删除终端（先下线）；purge=true 同时删除其状态文件。至少保留一个终端
func (m *Manager) Delete(id string, purge bool) error {
	m.addMu.Lock()
	defer m.addMu.Unlock()
	m.mu.Lock()
	idx := -1
	for i, t := range m.terms {
		if t.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		m.mu.Unlock()
		return fmt.Errorf("终端 #%s 不存在", id)
	}
	if len(m.terms) == 1 {
		m.mu.Unlock()
		return fmt.Errorf("至少保留一个终端")
	}
	t := m.terms[idx]
	m.terms = append(m.terms[:idx:idx], m.terms[idx+1:]...)
	if m.def == id {
		m.def = m.terms[0].ID
	}
	m.mu.Unlock()
	scenarios.Stop(id) // 正在运行的场景先停止
	t.Stop("删除终端")
	close(t.quit)
	t.stopStreams()
	t.mu.Lock()
	f := t.stateFile()
	if purge {
		t.dirty = false
	} else {
		t.save()
	}
	t.mu.Unlock()
	if purge {
		if f != "" && !m.phoneUsed(t.phoneNow(), nil) {
			os.Remove(f)
		}
		removeMediaFiles(id) // 媒体源文件按终端编号命名，不清理会永久残留（每个最大 64MB）
	}
	m.save()
	hub.add(LogEntry{Dir: "sys", Summary: fmt.Sprintf("已删除终端 #%s phone=%s", id, t.phoneNow())})
	return nil
}

// SetDefault 设置默认终端（旧接口 /api/state 等以及命令行 -c 作用的终端）
func (m *Manager) SetDefault(id string) error {
	if m.Get(id) == nil || id == "" {
		return fmt.Errorf("终端 #%s 不存在", id)
	}
	m.mu.Lock()
	m.def = id
	m.mu.Unlock()
	m.save()
	return nil
}

// save 写终端清单（内容不变时不写）
func (m *Manager) save() {
	m.mu.Lock()
	reg := registry{Default: m.def, NextID: m.next}
	for _, t := range m.terms {
		reg.Terminals = append(reg.Terminals, termRec{ID: t.ID, Phone: t.phoneNow()})
	}
	b, _ := json.MarshalIndent(reg, "", " ")
	same := string(b) == string(m.saved)
	m.mu.Unlock()
	if same {
		return
	}
	os.MkdirAll(stateDir, 0o755)
	f := registryFile()
	if os.WriteFile(f+".tmp", b, 0o644) == nil && os.Rename(f+".tmp", f) == nil {
		m.mu.Lock()
		m.saved = b
		m.mu.Unlock()
	}
}

// SaveAll 立即保存全部终端状态与清单（进程退出前调用）
func (m *Manager) SaveAll() {
	for _, t := range m.List() {
		t.mu.Lock()
		t.save()
		t.mu.Unlock()
	}
	m.save()
	logDisk.Flush()
}

func (m *Manager) saveLoop() {
	for range time.Tick(5 * time.Second) {
		m.save()
	}
}

// TermBrief 终端切换栏用的摘要
type TermBrief struct {
	ID        string `json:"id"`
	Phone     string `json:"phone"`
	Plate     string `json:"plate"`
	Status    string `json:"status"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Version   string `json:"version"`
	Transport string `json:"transport"`
	Alarms    int    `json:"alarms"`
	Default   bool   `json:"default"`
}

func (m *Manager) Briefs() []TermBrief {
	def := m.Default()
	var out []TermBrief
	for _, t := range m.List() {
		t.mu.Lock()
		c := t.Cfg
		n := 0
		for w := t.alarmWord(); w != 0; w &= w - 1 {
			n++
		}
		out = append(out, TermBrief{ID: t.ID, Phone: c.Phone, Plate: c.Plate, Status: t.status, Host: c.Host, Port: c.Port,
			Version: c.Version, Transport: c.transport(), Alarms: n, Default: t.ID == def})
		t.mu.Unlock()
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, e1 := strconv.Atoi(out[i].ID)
		b, e2 := strconv.Atoi(out[j].ID)
		if e1 == nil && e2 == nil {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}
