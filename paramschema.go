package main

// 结构体参数的逐字段定义与编解码（Web 页面按字段编辑，存储仍为 hex / 十进制串）

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SField 结构体中的一个字段（大端）
type SField struct {
	Key  string         `json:"key"`
	Name string         `json:"name,omitempty"`
	Size int            `json:"size"`           // 字节数
	Kind string         `json:"kind,omitempty"` // "" 无符号整数 / "str" 定长字符串(GBK,补 0) / "bcd" BCD 数字串 / "hex"
	Unit string         `json:"unit,omitempty"`
	Help string         `json:"help,omitempty"`
	Min  int64          `json:"min,omitempty"`
	Max  int64          `json:"max,omitempty"` // 0 = 按字节数
	Opts []Opt          `json:"opts,omitempty"`
	Bits map[int]string `json:"bits,omitempty"`
	Subs []SSub         `json:"subs,omitempty"` // 按位段拆分（该字段本身无值）
	Auto bool           `json:"auto,omitempty"` // 编码时自动计算（列表计数）
	Def  any            `json:"def,omitempty"`
}

// SSub 整数字段内的位段
type SSub struct {
	Key   string         `json:"key"`
	Name  string         `json:"name"`
	Shift int            `json:"shift"`
	Width int            `json:"width"`
	Unit  string         `json:"unit,omitempty"`
	Help  string         `json:"help,omitempty"`
	Max   int64          `json:"max,omitempty"`
	Opts  []Opt          `json:"opts,omitempty"`
	Bits  map[int]string `json:"bits,omitempty"`
	Def   int64          `json:"def,omitempty"`
}

// Schema 结构体：固定字段 + 可选的重复项列表
type Schema struct {
	Fields   []SField `json:"fields"`
	Item     []SField `json:"item,omitempty"`
	ItemName string   `json:"itemName,omitempty"`
	Count    string   `json:"count,omitempty"` // "head"：首字段为项数；"chan"：0x0076 三个计数之和
	MaxItems int      `json:"maxItems,omitempty"`
	Fixed    bool     `json:"fixed,omitempty"` // 长度必须等于字段总长
}

func fieldsSize(fs []SField) int {
	n := 0
	for _, f := range fs {
		n += f.Size
	}
	return n
}

func (s *Schema) size() int { return fieldsSize(s.Fields) }

func (f *SField) maxVal() int64 {
	if f.Max > 0 {
		return f.Max
	}
	if f.Size >= 8 {
		return math.MaxInt64
	}
	return int64(1)<<(8*f.Size) - 1
}

func (s *SSub) maxVal() int64 {
	if s.Max > 0 {
		return s.Max
	}
	return int64(1)<<s.Width - 1
}

func beUint(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

func beBytes(v uint64, n int) []byte {
	b := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}

// decodeFields 按字段解析，结果写入 out
func decodeFields(fs []SField, b []byte, out map[string]any) {
	pos := 0
	for _, f := range fs {
		raw := make([]byte, f.Size)
		if pos < len(b) {
			copy(raw, b[pos:min(pos+f.Size, len(b))])
		}
		pos += f.Size
		switch f.Kind {
		case "str":
			out[f.Key] = strings.TrimRight(ungbk(raw), "\x00 ")
		case "bcd":
			out[f.Key] = hex.EncodeToString(raw)
		case "hex":
			out[f.Key] = hex.EncodeToString(raw)
		default:
			v := beUint(raw)
			if len(f.Subs) == 0 {
				out[f.Key] = v
				continue
			}
			for _, s := range f.Subs {
				out[s.Key] = (v >> s.Shift) & (uint64(1)<<s.Width - 1)
			}
		}
	}
}

func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case float64:
		if x != math.Trunc(x) {
			return 0, fmt.Errorf("需要整数")
		}
		return int64(x), nil
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case uint64:
		return int64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case string:
		s := strings.TrimSpace(x)
		if strings.HasPrefix(strings.ToLower(s), "0x") {
			n, err := strconv.ParseInt(s[2:], 16, 64)
			return n, err
		}
		return strconv.ParseInt(s, 10, 64)
	case nil:
		return 0, nil
	}
	return 0, fmt.Errorf("类型 %T 无效", v)
}

func defInt(d any) int64 {
	n, _ := toInt(d)
	return n
}

// encodeFields 按字段编码；值缺省时用字段默认值
func encodeFields(fs []SField, vals map[string]any, auto map[string]int64) ([]byte, error) {
	var out []byte
	for _, f := range fs {
		label := f.Name
		if label == "" {
			label = f.Key
		}
		v, has := vals[f.Key]
		switch f.Kind {
		case "str":
			s := fmt.Sprint(f.Def)
			if f.Def == nil {
				s = ""
			}
			if has {
				s = fmt.Sprint(v)
			}
			if len(gbk(s)) > f.Size {
				return nil, fmt.Errorf("「%s」最多 %d 字节", label, f.Size)
			}
			out = append(out, fixed(s, f.Size)...)
		case "bcd", "hex":
			s := ""
			if f.Def != nil {
				s = fmt.Sprint(f.Def)
			}
			if has {
				s = strings.TrimSpace(fmt.Sprint(v))
			}
			if f.Kind == "bcd" {
				s = strings.ReplaceAll(s, ":", "")
				if strings.Trim(s, "0123456789") != "" {
					return nil, fmt.Errorf("「%s」只能是数字", label)
				}
				if len(s) > f.Size*2 {
					return nil, fmt.Errorf("「%s」最多 %d 位数字", label, f.Size*2)
				}
				s = strings.Repeat("0", f.Size*2-len(s)) + s
			}
			b, err := hex.DecodeString(s)
			if err != nil || len(b) > f.Size {
				return nil, fmt.Errorf("「%s」需要 %d 字节 hex", label, f.Size)
			}
			out = append(out, hexBytes(s, f.Size)...)
		default:
			var word uint64
			if len(f.Subs) == 0 {
				n := defInt(f.Def)
				if a, ok := auto[f.Key]; ok && f.Auto {
					n = a
				} else if has {
					var err error
					if n, err = toInt(v); err != nil {
						return nil, fmt.Errorf("「%s」%v", label, err)
					}
				}
				if n < f.Min || n > f.maxVal() {
					return nil, fmt.Errorf("「%s」取值范围 %d~%d", label, f.Min, f.maxVal())
				}
				word = uint64(n)
			} else {
				for _, s := range f.Subs {
					n := s.Def
					if sv, ok := vals[s.Key]; ok {
						var err error
						if n, err = toInt(sv); err != nil {
							return nil, fmt.Errorf("「%s」%v", s.Name, err)
						}
					}
					if n < 0 || n > s.maxVal() {
						return nil, fmt.Errorf("「%s」取值范围 0~%d", s.Name, s.maxVal())
					}
					word |= uint64(n) << s.Shift
				}
			}
			out = append(out, beBytes(word, f.Size)...)
		}
	}
	return out, nil
}

// decodeStruct 字节 -> 字段值（列表在 "items"，超出定义的尾部字节在 "_rest"）
func (s *Schema) decode(b []byte) map[string]any {
	out := map[string]any{}
	decodeFields(s.Fields, b, out)
	pos := s.size()
	if len(s.Item) > 0 {
		n := 0
		switch s.Count {
		case "head":
			n = int(defInt(out[s.Fields[0].Key]))
		case "chan":
			n = int(defInt(out["total"]) + defInt(out["audio"]) + defInt(out["video"]))
		}
		isz := fieldsSize(s.Item)
		items := []map[string]any{}
		for i := 0; i < n && pos+isz <= len(b); i++ {
			it := map[string]any{}
			decodeFields(s.Item, b[pos:pos+isz], it)
			items = append(items, it)
			pos += isz
		}
		out["items"] = items
	}
	if pos < len(b) {
		out["_rest"] = hex.EncodeToString(b[pos:])
	}
	return out
}

// encode 字段值 -> 字节
func (s *Schema) encode(vals map[string]any) ([]byte, error) {
	if vals == nil {
		vals = map[string]any{}
	}
	auto := map[string]int64{}
	var items []map[string]any
	if len(s.Item) > 0 {
		switch x := vals["items"].(type) {
		case []any:
			for _, it := range x {
				m, ok := it.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("列表项格式无效")
				}
				items = append(items, m)
			}
		case []map[string]any:
			items = x
		}
		if s.MaxItems > 0 && len(items) > s.MaxItems {
			return nil, fmt.Errorf("最多 %d 项", s.MaxItems)
		}
		switch s.Count {
		case "head":
			auto[s.Fields[0].Key] = int64(len(items))
		case "chan": // 0x0076：按每项的通道类型统计 音视频 / 音频 / 视频 通道数
			for _, it := range items {
				switch defInt(it["type"]) {
				case 0:
					auto["total"]++
				case 1:
					auto["audio"]++
				default:
					auto["video"]++
				}
			}
		}
	}
	out, err := encodeFields(s.Fields, vals, auto)
	if err != nil {
		return nil, err
	}
	for i, it := range items {
		b, err := encodeFields(s.Item, it, nil)
		if err != nil {
			return nil, fmt.Errorf("第 %d 项：%v", i+1, err)
		}
		out = append(out, b...)
	}
	if r, ok := vals["_rest"].(string); ok && r != "" && len(s.Item) == 0 {
		b, err := hex.DecodeString(strings.ReplaceAll(r, " ", ""))
		if err != nil {
			return nil, fmt.Errorf("扩展字节需要 hex")
		}
		out = append(out, b...)
	}
	return out, nil
}

func schemaDefault(s *Schema) string {
	b, err := s.encode(nil)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// paramFields 参数当前值按结构解析；无结构定义返回 nil
func paramFields(id int, v string) map[string]any { return paramFieldsStd(false, id, v) }

func paramFieldsStd(yue bool, id int, v string) map[string]any {
	d := pdefStd(yue, id)
	if d == nil || d.Schema == nil {
		return nil
	}
	return d.Schema.decode(encodeValue(id, v))
}

// encodeParamFields 字段值 -> 参数存储值（并校验）
func encodeParamFields(id int, vals map[string]any) (string, error) {
	return encodeParamFieldsStd(false, id, vals)
}

// encodeParamFieldsStd 按主动安全标准（粤标 F364~F367 结构不同）编码
func encodeParamFieldsStd(yue bool, id int, vals map[string]any) (string, error) {
	d := pdefStd(yue, id)
	if d == nil || d.Schema == nil {
		return "", fmt.Errorf("参数 %04X 没有结构定义", id)
	}
	b, err := d.Schema.encode(vals)
	if err != nil {
		return "", fmt.Errorf("参数 %04X「%s」：%v", id, d.Name, err)
	}
	return validateParam(id, decodeParam(id, b))
}

// ---------------- 各结构定义 ----------------

func photoSchema(unitName, valName string, u0, u1 Opt) *Schema {
	return &Schema{Fields: []SField{{Key: "w", Size: 4, Subs: []SSub{
		{Key: "enable", Name: "拍照开关", Shift: 0, Width: 5, Bits: chBits5, Help: "勾选的摄像通道参与拍照"},
		{Key: "upload", Name: "上传标志", Shift: 8, Width: 5, Bits: chBits5, Help: "勾选=上传，不勾选=仅存储（JT/T 808 表 14/15：0 存储 1 上传）"},
		{Key: "unit", Name: unitName, Shift: 16, Width: 1, Opts: []Opt{u0, u1}},
		{Key: "interval", Name: valName, Shift: 17, Width: 15, Help: "0 不拍照"},
	}}}}
}

var (
	optEncMode = []Opt{{0, "CBR 固定码率"}, {1, "VBR 可变码率"}, {2, "ABR 平均码率"}}
	optAVRes   = []Opt{{0, "QCIF"}, {1, "CIF"}, {2, "WCIF"}, {3, "D1"}, {4, "WD1"}, {5, "720P"}, {6, "1080P"}}
	osdBits    = map[int]string{0: "日期和时间", 1: "车牌号码", 2: "逻辑通道号", 3: "经纬度", 4: "行驶记录速度", 5: "卫星定位速度", 6: "连续驾驶时间"}
)

func streamFields(prefix, label string, fr int64, br int64) []SField {
	return []SField{
		{Key: prefix + "Enc", Name: label + "编码模式", Size: 1, Opts: optEncMode},
		{Key: prefix + "Res", Name: label + "分辨率", Size: 1, Opts: optAVRes, Def: 1},
		{Key: prefix + "Gop", Name: label + "关键帧间隔", Size: 2, Unit: "帧", Min: 1, Max: 1000, Def: 25},
		{Key: prefix + "Fps", Name: label + "目标帧率", Size: 1, Unit: "帧/秒", Min: 1, Max: 120, Def: fr},
		{Key: prefix + "Kbps", Name: label + "目标码率", Size: 4, Unit: "kbps", Def: br},
	}
}

var avSchema = &Schema{Fixed: true, Fields: append(append(append(streamFields("rt", "实时流", 15, 512), streamFields("st", "存储流", 15, 512)...),
	SField{Key: "osd", Name: "OSD 字幕叠加", Size: 2, Bits: osdBits, Def: 3}),
	SField{Key: "audio", Name: "启用音频输出", Size: 1, Opts: []Opt{{0, "不启用"}, {1, "启用"}}, Def: 1})}

var chanSchema = &Schema{Count: "chan", ItemName: "通道", MaxItems: 32, Fields: []SField{
	{Key: "total", Name: "音视频通道总数", Size: 1, Auto: true},
	{Key: "audio", Name: "音频通道总数", Size: 1, Auto: true},
	{Key: "video", Name: "视频通道总数", Size: 1, Auto: true},
}, Item: []SField{
	{Key: "phys", Name: "物理通道号", Size: 1, Min: 1, Def: 1},
	{Key: "logic", Name: "逻辑通道号", Size: 1, Min: 1, Def: 1},
	{Key: "type", Name: "通道类型", Size: 1, Opts: []Opt{{0, "音视频"}, {1, "音频"}, {2, "视频"}}},
	{Key: "ptz", Name: "是否连接云台", Size: 1, Opts: []Opt{{0, "未连接"}, {1, "连接"}}, Help: "通道类型为 0 和 2 时有效"},
}}

var chanVideoSchema = &Schema{Count: "head", ItemName: "通道", MaxItems: 11, Fields: []SField{
	{Key: "n", Name: "单独设置的通道数量", Size: 1, Auto: true},
}, Item: func() []SField {
	fs := []SField{{Key: "logic", Name: "逻辑通道号", Size: 1, Min: 1, Def: 1}}
	fs = append(fs, streamFields("rt", "实时流", 15, 512)...)
	fs = append(fs, streamFields("st", "存储流", 15, 512)...)
	return append(fs, SField{Key: "osd", Name: "OSD 字幕叠加", Size: 2, Bits: osdBits, Def: 3})
}()}

var sleepSchema = func() *Schema {
	fs := []SField{
		{Key: "mode", Name: "休眠唤醒模式", Size: 1, Bits: map[int]string{0: "条件唤醒", 1: "定时唤醒", 2: "手动唤醒"}},
		{Key: "cond", Name: "唤醒条件类型", Size: 1, Bits: map[int]string{0: "紧急报警", 1: "碰撞侧翻报警", 2: "车辆开门"}, Help: "休眠唤醒模式含条件唤醒时有效"},
		{Key: "days", Name: "定时唤醒日", Size: 1, Bits: map[int]string{0: "周一", 1: "周二", 2: "周三", 3: "周四", 4: "周五", 5: "周六", 6: "周日"}},
		{Key: "flag", Name: "定时唤醒启用", Size: 1, Bits: map[int]string{0: "时间段1", 1: "时间段2", 2: "时间段3", 3: "时间段4"}},
	}
	for i := 1; i <= 4; i++ {
		fs = append(fs, SField{Key: fmt.Sprintf("on%d", i), Name: fmt.Sprintf("时间段%d 唤醒时间", i), Size: 2, Kind: "bcd", Help: "HHMM"},
			SField{Key: fmt.Sprintf("off%d", i), Name: fmt.Sprintf("时间段%d 关闭时间", i), Size: 2, Kind: "bcd", Help: "HHMM"})
	}
	return &Schema{Fixed: true, Fields: fs}
}()

var canSchema = &Schema{Fixed: true, Fields: []SField{
	{Key: "interval", Name: "此 ID 采集时间间隔", Size: 4, Unit: "ms", Help: "bit63-32，0 表示不采集"},
	{Key: "w", Size: 4, Subs: []SSub{
		{Key: "channel", Name: "CAN 通道号", Shift: 31, Width: 1, Opts: []Opt{{0, "CAN1"}, {1, "CAN2"}}},
		{Key: "frame", Name: "帧类型", Shift: 30, Width: 1, Opts: []Opt{{0, "标准帧"}, {1, "扩展帧"}}},
		{Key: "method", Name: "数据采集方式", Shift: 29, Width: 1, Opts: []Opt{{0, "原始数据"}, {1, "采集区间的计算值"}}},
		{Key: "canId", Name: "CAN 总线 ID", Shift: 0, Width: 29, Help: "bit28-0，可输入 0x 开头的十六进制"},
	}},
}}

var (
	optShot   = []Opt{{0, "不开启"}, {1, "定时拍照"}, {2, "定距拍照"}, {3, "保留"}}
	optPicRes = []Opt{{1, "352×288"}, {2, "704×288"}, {3, "704×576"}, {4, "640×480"}, {5, "1280×720"}, {6, "1920×1080"}}
	optVidRes = []Opt{{1, "CIF"}, {2, "HD1"}, {3, "D1"}, {4, "WD1"}, {5, "VGA"}, {6, "720P"}, {7, "1080P"}}
)

func b1(key, name, unit string, def int64, help ...string) SField {
	f := SField{Key: key, Name: name, Size: 1, Unit: unit, Def: def}
	if len(help) > 0 {
		f.Help = help[0]
	}
	return f
}

// alarmGroup 某类报警的 分级速度阈值 / 前后视频录制时间 / 拍照张数 / 拍照间隔
func alarmGroup(key, name string) []SField {
	return []SField{
		b1(key+"Speed", name+"分级速度阈值", "km/h", 50, "超过该速度为二级报警"),
		b1(key+"Video", name+"前后视频录制时间", "秒", 5, "0 不录像"),
		b1(key+"Photos", name+"拍照张数", "张", 3, "0 不抓拍"),
		b1(key+"PhotoInt", name+"拍照间隔", "100ms", 2),
	}
}

func commonSafety(volName string, shot []Opt) []SField {
	return []SField{
		b1("speed", "报警判断速度阈值", "km/h", 30, "高于该速度才判断报警，0~60"),
		b1("volume", volName, "", 6, "0~8，8 最大"),
		{Key: "shot", Name: "主动拍照策略", Size: 1, Opts: shot},
		{Key: "shotInt", Name: "主动定时拍照时间间隔", Size: 2, Unit: "秒", Def: 60, Help: "0~3600，0 不抓拍"},
		{Key: "shotDist", Name: "主动定距拍照距离间隔", Size: 2, Unit: "米", Def: 200, Help: "0~60000，0 不抓拍"},
		b1("shotCount", "单次主动拍照张数", "张", 3, "1~10"),
		b1("shotGap", "单次主动拍照时间间隔", "100ms", 2, "1~5"),
		{Key: "picRes", Name: "拍照分辨率", Size: 1, Opts: optPicRes, Def: 1},
		{Key: "vidRes", Name: "视频录制分辨率", Size: 1, Opts: optVidRes, Def: 1},
	}
}

var adasSchema = &Schema{Fields: func() []SField {
	fs := commonSafety("报警提示音量", optShot)
	fs = append(fs,
		SField{Key: "alarmEn", Name: "报警使能", Size: 4, Def: 0x00010FFF, Bits: map[int]string{0: "障碍检测一级", 1: "障碍检测二级",
			2: "频繁变道一级", 3: "频繁变道二级", 4: "车道偏离一级", 5: "车道偏离二级", 6: "前向碰撞一级", 7: "前向碰撞二级",
			8: "行人碰撞一级", 9: "行人碰撞二级", 10: "车距过近一级", 11: "车距过近二级", 16: "道路标识超限"}},
		SField{Key: "eventEn", Name: "事件使能", Size: 4, Def: 3, Bits: map[int]string{0: "道路标识识别", 1: "主动拍照"}},
		b1("rsv1", "预留字段", "", 0),
		b1("obstDist", "障碍物报警距离阈值", "100ms", 30, "10~50"))
	fs = append(fs, alarmGroup("obst", "障碍物报警")...)
	fs = append(fs, b1("lcTime", "频繁变道报警判断时间段", "秒", 60, "30~120"), b1("lcCount", "频繁变道报警判断次数", "次", 5, "3~10"))
	fs = append(fs, alarmGroup("lc", "频繁变道报警")...)
	fs = append(fs, alarmGroup("ldw", "车道偏离报警")...)
	fs = append(fs, b1("fcwTime", "前向碰撞报警时间阈值", "100ms", 27, "10~50"))
	fs = append(fs, alarmGroup("fcw", "前向碰撞报警")...)
	fs = append(fs, b1("pcwTime", "行人碰撞报警时间阈值", "100ms", 30, "10~50"))
	fs = append(fs, alarmGroup("pcw", "行人碰撞报警")...)
	fs[len(fs)-4].Name, fs[len(fs)-4].Help = "行人碰撞报警使能速度阈值", "低于该速度才报警"
	fs = append(fs, b1("hmwDist", "车距监控报警距离阈值", "100ms", 10, "10~50"))
	fs = append(fs, alarmGroup("hmw", "车距过近报警")...)
	fs = append(fs, b1("tsrPhotos", "道路标志识别拍照张数", "张", 3), b1("tsrPhotoInt", "道路标志识别拍照间隔", "100ms", 2),
		SField{Key: "rsv2", Name: "保留字段", Size: 4, Kind: "hex", Help: "BYTE[4]；粤标为实线变道报警参数"})
	return fs
}()}

var adasDefault = schemaDefault(adasSchema)

var dsmSchema = &Schema{Fields: func() []SField {
	fs := commonSafety("报警音量", []Opt{{0, "不开启"}, {1, "定时拍照"}, {2, "定距拍照"}, {3, "插卡触发"}, {4, "保留"}})
	fs = append(fs,
		SField{Key: "alarmEn", Name: "报警使能", Size: 4, Def: 0x000003FF, Bits: map[int]string{0: "疲劳驾驶一级", 1: "疲劳驾驶二级",
			2: "接打电话一级", 3: "接打电话二级", 4: "抽烟一级", 5: "抽烟二级", 6: "分神驾驶一级", 7: "分神驾驶二级",
			8: "驾驶员异常一级", 9: "驾驶员异常二级"}},
		SField{Key: "eventEn", Name: "事件使能", Size: 4, Def: 3, Bits: map[int]string{0: "驾驶员更换", 1: "主动拍照"}},
		SField{Key: "smokeInt", Name: "吸烟报警判断时间间隔", Size: 2, Unit: "秒", Def: 180, Help: "0~3600"},
		SField{Key: "phoneInt", Name: "接打电话报警判断时间间隔", Size: 2, Unit: "秒", Def: 120, Help: "0~3600"},
		SField{Key: "rsv1", Name: "预留字段", Size: 3, Kind: "hex"})
	fs = append(fs, alarmGroup("fatigue", "疲劳驾驶报警")...)
	fs = append(fs, alarmGroup("phone", "接打电话报警")...)
	fs = append(fs, alarmGroup("smoke", "抽烟报警")...)
	fs = append(fs, alarmGroup("distract", "分神驾驶报警")...)
	fs = append(fs, alarmGroup("abnormal", "驾驶行为异常")...)
	fs = append(fs, SField{Key: "idTrigger", Name: "驾驶员身份识别触发", Size: 1,
		Opts: []Opt{{0, "不开启"}, {1, "定时触发"}, {2, "定距触发"}, {3, "插卡开始行驶触发"}, {4, "保留"}}})
	// 粤标扩展（共 65 字节，与网关 Schema 一致）
	ext := len(fs)
	fs = append(fs, b1("coverSpeed", "摄像机遮挡报警分级速度阈值（粤标）", "km/h", 0))
	fs = append(fs, alarmGroup("belt", "不系安全带报警（粤标）")...)
	fs = append(fs, alarmGroup("ir", "红外墨镜阻断失效报警（粤标）")...)
	fs = append(fs, alarmGroup("hands", "双脱把报警（粤标）")...)
	fs = append(fs, alarmGroup("play", "玩手机报警（粤标）")...)
	fs = append(fs, b1("rsv2", "保留字段（粤标）", "", 0))
	for i := ext; i < len(fs); i++ { // 粤标扩展默认 0
		fs[i].Def = int64(0)
	}
	return fs
}()}

var dsmDefault = schemaDefault(dsmSchema)

var tpmsSchema = &Schema{Fields: []SField{
	{Key: "tire", Name: "轮胎规格型号", Size: 12, Kind: "str", Def: "900R20", Help: "如 195/65R1591V，最多 12 个字符"},
	{Key: "unit", Name: "胎压单位", Size: 2, Opts: []Opt{{0, "kg/cm²"}, {1, "bar"}, {2, "kPa"}, {3, "PSI"}}, Def: 3},
	{Key: "normal", Name: "正常胎压值", Size: 2, Def: 140, Help: "单位同胎压单位"},
	{Key: "imbalance", Name: "胎压不平衡报警阈值", Size: 2, Unit: "%", Def: 20, Max: 100},
	{Key: "slowLeak", Name: "慢漏气报警阈值", Size: 2, Unit: "%", Def: 5, Max: 100},
	{Key: "low", Name: "低压报警阈值", Size: 2, Def: 110, Help: "单位同胎压单位"},
	{Key: "high", Name: "高压报警阈值", Size: 2, Def: 189, Help: "单位同胎压单位"},
	{Key: "temp", Name: "高温报警阈值", Size: 2, Unit: "℃", Def: 80},
	{Key: "volt", Name: "电压报警阈值", Size: 2, Unit: "%", Def: 10, Max: 100},
	{Key: "report", Name: "定时上报时间间隔", Size: 2, Unit: "秒", Def: 60, Max: 3600, Help: "0 不上报"},
	{Key: "rsv", Name: "保留项", Size: 6, Kind: "hex"},
}}

var tpmsDefault = schemaDefault(tpmsSchema)
