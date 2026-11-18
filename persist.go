package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// overlay 把 src 中非零字段覆盖到 c（布尔字段不覆盖，由页面/命令修改）
func (c *Config) overlay(src Config) {
	dv, sv := reflect.ValueOf(c).Elem(), reflect.ValueOf(src)
	for i := 0; i < sv.NumField(); i++ {
		f := sv.Field(i)
		if f.Kind() != reflect.Bool && !f.IsZero() {
			dv.Field(i).Set(f)
		}
	}
}

func (t *Terminal) fillDefaults() {
	c := &t.Cfg
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&c.Host, "127.0.0.1")
	def(&c.Version, "2019")
	def(&c.Plate, "粤A12345")
	def(&c.Model, "SIM-T808")
	def(&c.Maker, "SIMMK")
	def(&c.ICCID, "89860000000000000001")
	def(&c.IMEI, "860000000000001")
	def(&c.HwVer, "HW1.0")
	def(&c.FwVer, "FW1.0.0")
	if c.Port == 0 {
		c.Port = 7611
	}
	if t.Rec.Install == "" {
		t.Rec = RecorderState{Install: "240101080000", Pulse: 3600, Initial: 0, Class: "大型货车"}
	}
	if c.DeviceID == "" {
		c.DeviceID = c.Phone
		if len(c.DeviceID) > 7 {
			c.DeviceID = c.DeviceID[len(c.DeviceID)-7:]
		}
	}
	for id, v := range defaultParams() {
		if _, ok := t.Params[id]; !ok {
			t.Params[id] = v
		}
	}
	t.Params[0x0083] = c.Plate
	t.Params[0x0084] = itoa(c.PlateColor)
	t.gdFill()
}

func (t *Terminal) stateFile() string {
	if t.Cfg.Phone == "" {
		return ""
	}
	return filepath.Join(stateDir, t.Cfg.Phone+".json")
}

func (t *Terminal) load() {
	f := t.stateFile()
	if f == "" {
		return
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return
	}
	var s Saved
	if err := json.Unmarshal(b, &s); err != nil {
		t.errf("读取 %s 失败: %v（使用默认值）", f, err)
		return
	}
	if s.Params == nil {
		s.Params = defaultParams()
	}
	if s.Events == nil {
		s.Events = map[int]string{}
	}
	if s.Menus == nil {
		s.Menus = map[int]string{}
	}
	t.Saved = s
	t.rsaKey = nil
	t.rp = replayRun{}
	t.logf("已加载终端状态 %s（参数 %d 项，区域 %d 个，多媒体 %d 条，盲区 %d 条）", f, len(s.Params), len(s.Areas), len(s.Media), len(s.Blind))
}

// save 调用方持有 t.mu
func (t *Terminal) save() {
	f := t.stateFile()
	if f == "" {
		return
	}
	b, err := json.MarshalIndent(&t.Saved, "", " ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.errf("创建状态目录失败: %v", err)
		return
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.errf("写入状态失败: %v", err)
		return
	}
	if err := os.Rename(tmp, f); err != nil {
		t.errf("替换状态文件失败: %v", err)
		return
	}
	t.dirty = false
}

func (t *Terminal) markDirty() { t.dirty = true }

func (t *Terminal) saveLoop() {
	tk := time.NewTicker(5 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-t.quit:
			return
		case <-tk.C:
		}
		t.mu.Lock()
		if t.dirty {
			t.save()
		}
		t.mu.Unlock()
	}
}

// Profile 终端档案（页面导出/导入）。字段与 state/<phone>.json 同名，因此也可直接导入状态文件。
// 不含运行记录（收件箱、多媒体、盲区）与终端 RSA 私钥。
type Profile struct {
	Format    string           `json:"format"`
	Exported  string           `json:"exported,omitempty"`
	Cfg       Config           `json:"cfg"`
	Params    Params           `json:"params"`
	Lng       float64          `json:"lng"`
	Lat       float64          `json:"lat"`
	Mileage   float64          `json:"mileage"`
	Fuel      float64          `json:"fuel"`
	StatusOn  uint32           `json:"statusOn"`
	ExtSignal uint32           `json:"extSignal"`
	IOStatus  uint16           `json:"ioStatus"`
	Analog    [2]int           `json:"analog"`
	Driver    Driver           `json:"driver"`
	Rec       RecorderState    `json:"rec"`
	Areas     []Area           `json:"areas"`
	Phonebook []map[string]any `json:"phonebook"`
	Events    map[int]string   `json:"events"`
	Menus     map[int]string   `json:"menus"`
	PlatRSA   PlatKey          `json:"platRsa"`
	Replay    *Replay          `json:"replay,omitempty"`
	Tires     []int            `json:"tires,omitempty"`
	CabinTemp *int             `json:"cabinTemp,omitempty"`
}

const profileFormat = "jt808sim-profile/1"

func (t *Terminal) exportProfile() Profile {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.Saved
	return Profile{Format: profileFormat, Exported: time.Now().Format(time.RFC3339), Cfg: s.Cfg, Params: s.Params,
		Lng: s.Lng, Lat: s.Lat, Mileage: s.Mileage, Fuel: s.Fuel, StatusOn: s.StatusOn, ExtSignal: s.ExtSignal,
		IOStatus: s.IOStatus, Analog: s.Analog, Driver: s.Driver, Rec: s.Rec, Areas: s.Areas, Phonebook: s.Phonebook,
		Events: s.Events, Menus: s.Menus, PlatRSA: s.PlatRSA, Replay: s.Replay, Tires: s.Tires, CabinTemp: s.CabinTemp}
}

// importProfile 导入档案（必须离线）。参数逐项校验，未知参数按原始 hex 保留；缺失的参数用默认值补齐
func (t *Terminal) importProfile(p Profile) error {
	if p.Cfg.Phone == "" && len(p.Params) == 0 {
		return fmt.Errorf("不是有效的终端档案（缺少 cfg/params）")
	}
	params := defaultParams()
	for id, v := range p.Params {
		nv, err := validateParam(id, v)
		if err != nil { // 超出页面校验范围但格式正确的值（如平台 8103 下发的）原样保留
			if _, terr := validateType(id, v); terr != nil {
				return fmt.Errorf("导入失败：%v", terr)
			}
			nv = v
		}
		params[id] = nv
	}
	if p.Cfg.Version != "" && p.Cfg.Version != "2013" && p.Cfg.Version != "2019" {
		return fmt.Errorf("导入失败：version 只能是 2013/2019")
	}
	if strings.Trim(p.Cfg.Phone, "0123456789") != "" {
		return fmt.Errorf("导入失败：手机号只能是数字")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.want {
		return fmt.Errorf("请先断开连接再导入")
	}
	if p.Cfg.Phone != "" && p.Cfg.Phone != t.Cfg.Phone && t.mgr.phoneUsed(p.Cfg.Phone, t) {
		return fmt.Errorf("导入失败：手机号 %s 已被其它终端使用", p.Cfg.Phone)
	}
	if p.Cfg.Phone != "" && p.Cfg.Phone != t.Cfg.Phone {
		t.save() // 先保存当前终端
	}
	s := &t.Saved
	s.Cfg, s.Params = p.Cfg, params
	if p.Lng != 0 || p.Lat != 0 {
		s.Lng, s.Lat = p.Lng, p.Lat
	}
	s.Mileage, s.Fuel, s.StatusOn = p.Mileage, p.Fuel, p.StatusOn
	s.Tires, s.CabinTemp = p.Tires, p.CabinTemp
	s.ExtSignal, s.IOStatus, s.Analog = p.ExtSignal, p.IOStatus, p.Analog
	if p.Driver.Name != "" {
		s.Driver = p.Driver
	}
	if p.Rec.Install != "" {
		s.Rec = p.Rec
	}
	s.Areas, s.Phonebook, s.PlatRSA = p.Areas, p.Phonebook, p.PlatRSA
	if p.Replay != nil && len(p.Replay.Points) >= 2 {
		s.Replay = p.Replay
	}
	t.rp = replayRun{}
	s.Events, s.Menus = p.Events, p.Menus
	if s.Events == nil {
		s.Events = map[int]string{}
	}
	if s.Menus == nil {
		s.Menus = map[int]string{}
	}
	t.track, t.inArea, t.segIn = nil, map[int]bool{}, map[int]*segVisit{}
	if t.Cfg.Phone == "" {
		t.Cfg.Phone = "13800000001"
	}
	t.Cfg.Plate = firstNonEmpty(t.Cfg.Plate, params[0x0083]) // 车牌以 cfg 为准，0x0083 随之同步
	t.fillDefaults()
	t.phone.Store(t.Cfg.Phone)
	t.save()
	t.logf("已导入终端档案 phone=%s（参数 %d 项，区域 %d 个）", t.Cfg.Phone, len(t.Params), len(t.Areas))
	return nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
