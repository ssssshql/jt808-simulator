package main

// 动作层：页面 POST /api/action 与命令行共用

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func (t *Terminal) Do(typ string, p map[string]any) error {
	switch typ {
	case "setConfig":
		return t.setConfig(p)
	case "connect":
		t.mu.Lock()
		if t.Cfg.Phone == "" {
			t.mu.Unlock()
			return fmt.Errorf("请先填写终端手机号")
		}
		t.powerOff = false
		t.mu.Unlock()
		t.Start()
	case "disconnect":
		t.Stop("手动断开")
	case "logout":
		return t.logout()
	case "setMotion":
		t.mu.Lock()
		if m := pstr(p, "mode", ""); m != "" {
			if !oneOf(m, "stop", "drive", "circle", "route", "track") {
				t.mu.Unlock()
				return fmt.Errorf("mode 只能是 stop/drive/circle/route/track")
			}
			if m == "track" && (t.Replay == nil || len(t.Replay.Points) < 2) {
				t.mu.Unlock()
				return fmt.Errorf("请先导入轨迹（GPX / CSV）")
			}
			if m == "track" && t.motion.Mode != "track" {
				if !t.rp.ready || t.rp.done {
					t.seekReplay(0)
				} else {
					t.applyReplayPos(false)
				}
			}
			t.motion.Mode, t.motion.center = m, [2]float64{}
		}
		if _, ok := p["speed"]; ok {
			t.motion.Speed = float64(max(min(pint(p, "speed", 60), 220), 0))
		}
		t.mu.Unlock()
	case "setPosition":
		lng, err1 := strconv.ParseFloat(pstr(p, "lng", ""), 64)
		lat, err2 := strconv.ParseFloat(pstr(p, "lat", ""), 64)
		if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
			return fmt.Errorf("经纬度无效")
		}
		t.mu.Lock()
		t.Lng, t.Lat, t.track, t.motion.center = lng, lat, nil, [2]float64{}
		t.markDirty()
		t.mu.Unlock()
		t.reportNow()
	case "setVehicle":
		t.mu.Lock()
		if v, err := strconv.ParseFloat(pstr(p, "mileage", ""), 64); err == nil && v >= 0 {
			t.Mileage = v
			t.Params[0x0080] = itoa(int(v * 10))
		}
		if v, err := strconv.ParseFloat(pstr(p, "fuel", ""), 64); err == nil && v >= 0 {
			t.Fuel = v
		}
		if v, ok := p["tires"]; ok { // "250,250,240" 或 [250, 250]；空 = 不附带 0x05
			list, err := parseTires(v)
			if err != nil {
				t.mu.Unlock()
				return err
			}
			t.Tires = list
		}
		if v, ok := p["cabinTemp"]; ok { // 空 = 不附带 0x06
			if s := strings.TrimSpace(fmt.Sprint(v)); s == "" || v == nil {
				t.CabinTemp = nil
			} else if n, err := strconv.Atoi(strings.TrimSuffix(s, ".0")); err == nil && n >= -32767 && n <= 32767 {
				t.CabinTemp = &n
			} else {
				t.mu.Unlock()
				return fmt.Errorf("车厢温度需要 -32767~32767 的整数")
			}
		}
		t.markDirty()
		t.mu.Unlock()
	case "report":
		t.reportNow()
	case "setStatus", "setAlarm", "setVideoAlarm":
		return t.setBit(typ, pint(p, "bit", -1), pint(p, "on", 0) == 1)
	case "clearAlarms":
		t.mu.Lock()
		t.alarmMan, t.videoMan, t.pendEvt = 0, 0, 0
		t.alarmClr |= t.alarmAuto
		t.contDrive, t.parkSec, t.overSec = 0, 0, 0
		t.mu.Unlock()
		t.reportNow()
	case "send":
		id, err := strconv.ParseInt(strings.TrimPrefix(pstr(p, "msgId", ""), "0x"), 16, 32)
		if err != nil {
			return fmt.Errorf("msgId 无效")
		}
		params, _ := p["params"].(map[string]any)
		if params == nil {
			params = map[string]any{}
		}
		return t.sendUplink(int(id), params)
	case "adas":
		// 报警类型用 alarmType（页面 POST 的 type 字段是动作名）；兼容命令行/旧调用的 type
		return t.raiseADAS(pstr(p, "kind", "adas"), pint(p, "alarmType", pint(p, "type", 1)), pint(p, "level", 1), pint(p, "files", 3), pint(p, "flag", -1))
	case "gdPeriph", "gdReport", "gdSet":
		return t.gdAction(typ, p)
	case "setParam":
		id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(pstr(p, "id", "")), "0x"), 16, 32)
		if err != nil {
			return fmt.Errorf("参数 ID 无效")
		}
		var v string
		t.mu.Lock()
		yue := t.Cfg.yue()
		t.mu.Unlock()
		if d := pdefStd(yue, int(id)); d != nil && d.ReadOnly {
			return fmt.Errorf("参数 %04X 只支持查询，不能设置", id)
		}
		if f, ok := p["fields"].(map[string]any); ok {
			v, err = encodeParamFieldsStd(yue, int(id), f)
		} else {
			v, err = validateParam(int(id), pstr(p, "value", ""))
		}
		if err != nil {
			return err
		}
		t.mu.Lock()
		t.applyParam(int(id), v)
		t.mu.Unlock()
	case "setParams": // 批量保存：{"values": {"0001": "30", ...}, "fields": {"0075": {...}}}，全部校验通过才生效
		set := map[int]string{}
		t.mu.Lock()
		yue := t.Cfg.yue()
		t.mu.Unlock()
		if vals, ok := p["values"].(map[string]any); ok {
			for k, raw := range vals {
				id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(k), "0x"), 16, 32)
				if err != nil {
					return fmt.Errorf("参数 ID %q 无效", k)
				}
				v, err := validateParam(int(id), pstr(p["values"].(map[string]any), k, fmt.Sprint(raw)))
				if err != nil {
					return err
				}
				set[int(id)] = v
			}
		}
		if fs, ok := p["fields"].(map[string]any); ok {
			for k, raw := range fs {
				id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(k), "0x"), 16, 32)
				f, ok := raw.(map[string]any)
				if err != nil || !ok {
					return fmt.Errorf("参数 %q 字段无效", k)
				}
				v, err := encodeParamFieldsStd(yue, int(id), f)
				if err != nil {
					return err
				}
				set[int(id)] = v
			}
		}
		t.mu.Lock()
		for id, v := range set {
			t.applyParam(id, v)
		}
		t.mu.Unlock()
		t.logf("页面保存参数 %d 项", len(set))
	case "deleteParam":
		id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(pstr(p, "id", "")), "0x"), 16, 32)
		if err != nil {
			return fmt.Errorf("参数 ID 无效")
		}
		if paramIndex[int(id)] != nil || gdParamIndex[int(id)] != nil && t.Cfg.yue() {
			return fmt.Errorf("标准参数不能删除，可恢复默认值")
		}
		t.mu.Lock()
		delete(t.Params, int(id))
		t.markDirty()
		t.mu.Unlock()
	case "resetParams":
		t.mu.Lock()
		t.Params = defaultParams()
		t.Params[0x0080] = itoa(int(t.Mileage * 10))
		t.fillDefaults()
		t.markDirty()
		t.mu.Unlock()
		t.logf("终端参数已恢复默认值")
	case "setSignal", "setIO":
		return t.setBit(typ, pint(p, "bit", -1), pint(p, "on", 0) == 1)
	case "setAnalog":
		t.mu.Lock()
		for i, k := range []string{"ad0", "ad1"} {
			if _, ok := p[k]; ok {
				t.Analog[i] = max(0, min(0xFFFF, pint(p, k, t.Analog[i])))
			}
		}
		t.markDirty()
		t.mu.Unlock()
		t.reportNow()
	case "setDriver":
		t.mu.Lock()
		d := &t.Driver
		d.Name, d.LicenseNo, d.Org = pstr(p, "name", d.Name), pstr(p, "licenseNo", d.LicenseNo), pstr(p, "org", d.Org)
		d.IDCard = pstr(p, "idCard", d.IDCard)
		if v := pstr(p, "validTo", d.ValidTo); len(v) == 8 && strings.Trim(v, "0123456789") == "" {
			d.ValidTo = v
		} else {
			t.mu.Unlock()
			return fmt.Errorf("有效期需要 8 位数字 YYYYMMDD")
		}
		t.markDirty()
		t.mu.Unlock()
	case "setRecorder":
		t.mu.Lock()
		r := &t.Rec
		if v := pstr(p, "install", r.Install); len(v) == 12 && strings.Trim(v, "0123456789") == "" {
			r.Install = v
		} else {
			t.mu.Unlock()
			return fmt.Errorf("安装日期需要 12 位数字 YYMMDDhhmmss")
		}
		r.Pulse, r.Initial = max(0, min(0xFFFF, pint(p, "pulse", r.Pulse))), max(0, pint(p, "initial", r.Initial))
		r.Class = pstr(p, "class", r.Class)
		t.markDirty()
		t.mu.Unlock()
	case "deleteArea":
		t.mu.Lock()
		var ids []int
		if id := pint(p, "id", -1); id >= 0 {
			ids = []int{id}
		}
		n := 0
		if k := pint(p, "kind", 0); k > 0 {
			n = t.deleteAreas(k, ids)
		} else {
			for k := 1; k <= 4; k++ {
				n += t.deleteAreas(k, nil)
			}
		}
		t.mu.Unlock()
		t.logf("页面删除区域 %d 个", n)
	case "setReply":
		t.mu.Lock()
		if m := pstr(p, "mode", ""); m != "" {
			if !oneOf(m, "auto", "fail", "unsupported", "none") {
				t.mu.Unlock()
				return fmt.Errorf("mode 只能是 auto/fail/unsupported/none")
			}
			t.replyMode = m
		}
		if d, err := strconv.ParseFloat(pstr(p, "delay", ""), 64); err == nil && d >= 0 && d <= 120 {
			t.replyDelay = d
		}
		t.logf("下行应答策略=%s 延迟=%vs", t.replyMode, t.replyDelay)
		t.mu.Unlock()
	case "answer":
		return t.answer(pint(p, "serial", -1), pint(p, "answerId", 0))
	case "raw":
		return t.raw(pstr(p, "line", ""))
	case "stopStream":
		if !t.stopStream(pint(p, "channel", 0)) {
			return fmt.Errorf("该通道没有推流")
		}
	case "setReplay", "clearReplay":
		return t.replayAction(typ, p)
	case "photo":
		go t.capture(max(pint(p, "channel", 1), 1), 0, false, 2)
	default:
		return fmt.Errorf("未知动作 %s", typ)
	}
	return nil
}

func (t *Terminal) setConfig(p map[string]any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	online := t.want
	c := t.Cfg
	set := func(k string, dst *string) {
		if v, ok := p[k]; ok {
			*dst = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	for k, dst := range map[string]*string{"host": &c.Host, "phone": &c.Phone, "version": &c.Version, "token": &c.Token,
		"plate": &c.Plate, "vin": &c.VIN, "deviceId": &c.DeviceID, "model": &c.Model, "maker": &c.Maker, "iccid": &c.ICCID,
		"imei": &c.IMEI, "fwVer": &c.FwVer, "hwVer": &c.HwVer, "attachHost": &c.AttachHost} {
		set(k, dst)
	}
	if v, ok := p["transport"]; ok {
		c.Transport = strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
		if c.Transport != "tcp" && c.Transport != "udp" {
			return fmt.Errorf("transport 只能是 tcp/udp")
		}
	}
	if v, ok := p["safetyStd"]; ok {
		s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
		switch s {
		case "", "jsatl", "su", "苏标":
			c.SafetyStd = ""
		case StdGD, "粤标":
			c.SafetyStd = StdGD
		default:
			return fmt.Errorf("safetyStd 只能是 jsatl（苏标）/gd（粤标）")
		}
	}
	if v, ok := p["reply8500"]; ok {
		c.Reply8500 = strings.TrimSpace(fmt.Sprint(v))
		if c.Reply8500 == "0500" {
			c.Reply8500 = ""
		}
		if c.Reply8500 != "" && c.Reply8500 != "ack0500" {
			return fmt.Errorf("reply8500 只能是 0500/ack0500")
		}
	}
	if _, ok := p["port"]; ok {
		c.Port = pint(p, "port", c.Port)
	}
	if _, ok := p["plateColor"]; ok {
		c.PlateColor = pint(p, "plateColor", c.PlateColor)
	}
	for k, dst := range map[string]*bool{"autoReconnect": &c.AutoReconnect, "autoAttach": &c.AutoAttach, "upRsa": &c.UpRSA} {
		if _, ok := p[k]; ok {
			*dst = pint(p, k, 0) == 1
		}
	}
	if c.Version != "2013" && c.Version != "2019" {
		return fmt.Errorf("version 只能是 2013/2019")
	}
	if c.Port <= 0 || c.Port > 65535 || c.Host == "" {
		return fmt.Errorf("服务器地址/端口无效")
	}
	if strings.Trim(c.Phone, "0123456789") != "" || len(c.Phone) > 20 {
		return fmt.Errorf("手机号只能是数字")
	}
	connKey := func(x Config) string {
		return x.Host + itoa(x.Port) + x.Phone + x.Version + itoa(x.ver()) + x.transport()
	}
	if online && connKey(c) != connKey(t.Cfg) {
		return fmt.Errorf("在线时不能修改服务器/传输方式/手机号/版本（粤标强制 2019，2013 终端切换标准也需先断开），请先断开")
	}
	stdChanged := c.yue() != t.Cfg.yue()
	if c.Phone != t.Cfg.Phone && t.mgr.phoneUsed(c.Phone, t) {
		return fmt.Errorf("手机号 %s 已被其它终端使用", c.Phone)
	}
	if c.Phone != t.Cfg.Phone && c.Phone != "" { // 切换手机号：先保存当前状态，再加载该手机号保存的状态
		if t.Cfg.Phone != "" {
			t.save()
		}
		t.Cfg.Phone = c.Phone
		t.load()
		cur := t.Cfg
		cur.overlay(c)
		c = cur
	}
	t.Cfg = c
	if stdChanged {
		t.gdSwitchParams(c.yue())
		t.logf("主动安全标准切换为 %s", c.stdName())
	}
	t.fillDefaults()
	t.phone.Store(t.Cfg.Phone)
	t.markDirty()
	return nil
}

func (t *Terminal) setBit(typ string, bit int, on bool) error {
	names := map[string]map[int]string{"setStatus": statusNames, "setAlarm": alarmNames, "setVideoAlarm": videoAlarmNames,
		"setSignal": extSignalNames, "setIO": ioNames}[typ]
	if names[bit] == "" {
		return fmt.Errorf("无效的位 %d", bit)
	}
	t.mu.Lock()
	flip := func(v *uint32) {
		if on {
			*v |= 1 << bit
		} else {
			*v &^= 1 << bit
		}
	}
	switch typ {
	case "setStatus":
		if statusComputed&(1<<bit) != 0 {
			t.mu.Unlock()
			return fmt.Errorf("「%s」由模拟器根据位置/速度计算，不能手动设置", names[bit])
		}
		flip(&t.StatusOn)
		t.markDirty()
	case "setAlarm":
		flip(&t.alarmMan)
		if on {
			t.alarmClr &^= 1 << bit
		}
		if bit == 0 && on { // 紧急报警需人工确认：附带 0x04 报警事件 ID
			t.alarmSeq++
			t.pendEvt = t.alarmSeq
		}
	case "setVideoAlarm":
		flip(&t.videoMan)
	case "setSignal":
		flip(&t.ExtSignal)
		t.markDirty()
	case "setIO":
		v := uint32(t.IOStatus)
		flip(&v)
		t.IOStatus = uint16(v)
		t.markDirty()
	}
	t.mu.Unlock()
	t.reportNow()
	return nil
}

// raw "<消息ID> <hex 消息体>"
func (t *Terminal) raw(line string) error {
	f := strings.Fields(line)
	if len(f) == 0 {
		return fmt.Errorf("用法: <消息ID> <hex 消息体>")
	}
	id, err := strconv.ParseInt(f[0], 16, 32)
	if err != nil {
		return err
	}
	body, err := hex.DecodeString(strings.Join(f[1:], ""))
	if err != nil {
		return err
	}
	_, _, err = t.send(int(id), body, "raw")
	return err
}

type ParamView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	Group string `json:"group"`
}

// paramViews 调用方持有 t.mu
func (t *Terminal) paramViews() []ParamView {
	var ids []int
	for id := range t.Params {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var out []ParamView
	for _, id := range ids {
		v := ParamView{ID: fmt.Sprintf("%04X", id), Type: paramType(id), Value: t.Params[id], Name: "未定义参数", Group: "其它"}
		if d := pdefStd(t.Cfg.yue(), id); d != nil {
			v.Name, v.Group = d.Name, d.Group
		}
		out = append(out, v)
	}
	return out
}

// ParamFull 页面参数编辑：定义 + 当前值 + 结构体解析结果
type ParamFull struct {
	ID string `json:"id"`
	ParamDef
	Value     string         `json:"value"`
	Fields    map[string]any `json:"fields,omitempty"`
	DefFields map[string]any `json:"defFields,omitempty"`
	Known     bool           `json:"known"`
}

var paramGroups = []string{"通信", "服务器", "汇报", "电话", "报警", "多媒体", "音视频", "车辆", "GNSS", "CAN", "主动安全", "其它"}

// paramFull 全部参数（标准参数 + 已保存的其它参数）。调用方持有 t.mu
func (t *Terminal) paramFull() []ParamFull {
	seen := map[int]bool{}
	var ids []int
	yue := t.Cfg.yue()
	for _, d := range paramDefs {
		ids, seen[d.ID] = append(ids, d.ID), true
	}
	if yue {
		ids, seen[0xF370] = append(ids, 0xF370), true
	}
	var extra []int
	for id := range t.Params {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Ints(extra)
	ids = append(ids, extra...)
	out := make([]ParamFull, 0, len(ids))
	for _, id := range ids {
		v, ok := t.Params[id]
		d := pdefStd(yue, id)
		f := ParamFull{ID: fmt.Sprintf("%04X", id), Value: v, Known: d != nil}
		if d != nil {
			f.ParamDef = *d
		} else {
			f.ParamDef = ParamDef{Name: "未定义参数（原始字节）", Type: "BYTES", Group: "其它"}
		}
		if !ok {
			f.Value = f.Def
		}
		f.Fields = paramFieldsStd(yue, id, f.Value)
		f.DefFields = paramFieldsStd(yue, id, f.Def)
		out = append(out, f)
	}
	return out
}

func oneOf(v string, opts ...string) bool {
	for _, o := range opts {
		if v == o {
			return true
		}
	}
	return false
}

// parseTires 胎压列表：逗号分隔字符串或数组，每个 0~254，最多 30 个
func parseTires(v any) ([]int, error) {
	var parts []string
	switch x := v.(type) {
	case nil:
	case string:
		parts = strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ' ' || r == '，' })
	case []any:
		for _, e := range x {
			parts = append(parts, fmt.Sprint(e))
		}
	default:
		return nil, fmt.Errorf("胎压格式无效")
	}
	if len(parts) > 30 {
		return nil, fmt.Errorf("胎压最多 30 个轮子")
	}
	var out []int
	for _, s := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 0 || n > 254 {
			return nil, fmt.Errorf("胎压 %q 无效（0~254，0xFF 表示无此轮）", s)
		}
		out = append(out, n)
	}
	return out, nil
}
