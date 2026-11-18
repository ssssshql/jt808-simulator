package main

// 粤标：T/GDRTA 002—2020《道路运输车辆智能视频监控报警系统 通讯协议规范》终端侧实现。
// 与苏标（JSATL12）的差异集中在这里，按终端配置 safetyStd = "gd" 启用；未启用时行为与原来完全一致。
//
//   5.1   协议基础按 JT/T 808-2019 → 粤标终端强制使用 2019 帧格式（Config.ver）
//   5.2   0x8900/0x0900 透传类型 0xF7 外设状态 / 0xF8 外设系统信息（表 5-3~5-7）
//   5.3   参数 0xF364~0xF367（表 5-10~5-13）、0xF370 智能视频协议版本（只读）
//   5.4   0200 附加信息 0x64~0x67（表 5-15~5-20）、0xF1/0xF2 安装/算法异常、40 字节报警标识号（表 5-16）
//   5.5~5.6 9208 / 1210 / 1211 / 码流 / 1212 / 9212、车辆状态数据记录文件（表 5-22）、附件命名
//   5.7   8105 命令字 1 无线升级（升级文件命名规则）、0x0108 升级结果、0x1FC4 升级进度
//   5.8   8801 通道 0x64/0x65 外设立即拍照
//   5.9   智能音视频通道 0x64/0x65/0x67（9101/9201），RTP SIM 卡号 BCD[10]

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const StdGD = "gd" // 粤标；空值（或 "jsatl"）= 苏标

func (c *Config) yue() bool { return c.SafetyStd == StdGD }

func (c *Config) stdName() string {
	if c.yue() {
		return "粤标 T/GDRTA 002-2020"
	}
	return "苏标 JSATL12"
}

// gdTypeNames 粤标报警/事件类型（表 5-15 / 5-17 / 5-19 / 5-20）。TPMS 为表 5-19 的位号 + 1
var gdTypeNames = map[int]map[int]string{
	0x64: {1: "前向碰撞报警", 2: "车道偏离报警", 3: "车距过近报警", 4: "行人碰撞报警", 5: "频繁变道报警", 6: "道路标识超限报警",
		7: "障碍物报警", 0x10: "道路标志识别事件", 0x11: "主动抓拍事件", 0x12: "实线变道报警", 0x13: "车厢过道行人检测报警"},
	0x65: {1: "疲劳驾驶报警", 2: "接打手持电话报警", 3: "抽烟报警", 4: "不目视前方报警", 5: "驾驶员异常报警", 6: "探头遮挡（摄像机遮挡）报警",
		8: "超时驾驶报警", 0x0A: "未系安全带报警", 0x0B: "红外阻断型墨镜失效报警", 0x0C: "双脱把报警（双手同时脱离方向盘）", 0x0D: "玩手机报警",
		0x10: "自动抓拍事件", 0x11: "驾驶员变更事件"},
	0x66: {1: "胎压（定时上报）", 2: "胎压过高报警", 3: "胎压过低报警", 4: "胎温过高报警", 5: "传感器异常报警", 6: "胎压不平衡报警",
		7: "慢漏气报警", 8: "电池电量低报警"},
	0x67: {1: "后方接近报警", 2: "左侧后方接近报警", 3: "右侧后方接近报警"},
}

// typeNames 当前标准的报警类型表
func (c *Config) typeNames() map[int]map[int]string {
	if c.yue() {
		return gdTypeNames
	}
	return adasTypeNames
}

// gdIsEvent 无开始/结束标志的事件类（标志状态填 0x00）
func gdIsEvent(attr, typ int) bool {
	switch attr {
	case 0x64, 0x65:
		return typ == 0x10 || typ == 0x11
	case 0x66:
		return typ == 1 // 胎压定时上报
	}
	return false
}

// ---------------- 外设（0x0900 透传 F7/F8、升级目标） ----------------

type GDPeriph struct {
	ID       int    `json:"id"`
	Work     int    `json:"work"`  // 表 5-6 工作状态：1 正常 2 待机 3 升级维护 4 设备异常 0x10 断开连接
	Alarm    uint32 `json:"alarm"` // 表 5-6 报警状态位
	Company  string `json:"company"`
	Model    string `json:"model"`
	HwVer    string `json:"hwVer"`
	SwVer    string `json:"swVer"`
	DevID    string `json:"devId"`
	Customer string `json:"customer"`
}

// GDState 粤标运行状态（随终端状态文件保存）
type GDState struct {
	Periph     []GDPeriph `json:"periph"`
	InstallErr uint32     `json:"installErr"` // 0xF1 安装异常信息（厂家自定义，非 0 时附带）
	AlgoErr    uint32     `json:"algoErr"`    // 0xF2 算法异常信息
	UpgradeSim string     `json:"upgradeSim"` // 下次升级的模拟结果，空 = 自动
	AlarmID    uint32     `json:"alarmId"`    // 报警 ID：从 0 循环累加，不区分类型
}

var gdPeriphNames = map[int]string{0x64: "ADAS", 0x65: "DSM", 0x66: "TPMS", 0x67: "BSD"}
var gdWorkNames = map[int]string{1: "正常工作", 2: "待机状态", 3: "升级维护", 4: "设备异常", 0x10: "断开连接"}
var gdPeriphAlarmBits = map[int]string{0: "摄像机异常", 1: "主存储器异常", 2: "辅存储器异常", 3: "红外补光异常", 4: "扬声器异常",
	5: "电池异常", 10: "通讯模块异常", 11: "定位模块异常"}

func gdDefaultPeriph() []GDPeriph {
	var out []GDPeriph
	for _, id := range []int{0x64, 0x65, 0x66, 0x67} {
		n := gdPeriphNames[id]
		out = append(out, GDPeriph{ID: id, Work: 1, Company: "SIMMK", Model: "SIM-" + n, HwVer: "HW1.0", SwVer: "SV1.0.0",
			DevID: fmt.Sprintf("%s%07d", n, id), Customer: "GD"})
	}
	return out
}

// periph 调用方持有 t.mu
func (t *Terminal) periph(id int) *GDPeriph {
	for i := range t.GD.Periph {
		if t.GD.Periph[i].ID == id {
			return &t.GD.Periph[i]
		}
	}
	return nil
}

// gdFill 补齐粤标运行状态与参数。调用方持有 t.mu
func (t *Terminal) gdFill() {
	if len(t.GD.Periph) == 0 {
		t.GD.Periph = gdDefaultPeriph()
	}
	if !t.Cfg.yue() {
		return
	}
	for _, id := range []int{0xF364, 0xF365, 0xF366, 0xF367} {
		t.Params[id] = gdNormalize(id, t.Params[id])
	}
	t.Params[0xF370] = gdProtoVersion
}

// gdNormalize 粤标参数长度对齐：等于苏标默认值的换成粤标默认值；长度不足的用粤标默认值补齐尾部
func gdNormalize(id int, v string) string {
	d := gdParamIndex[id]
	if d == nil {
		return v
	}
	if su := paramIndex[id]; v == "" || su != nil && v == su.Def {
		return d.Def
	}
	b, err := hex.DecodeString(v)
	n := d.Schema.size()
	if err != nil {
		return d.Def
	}
	if len(b) < n {
		def, _ := hex.DecodeString(d.Def)
		b = append(b, def[len(b):]...)
	}
	return hex.EncodeToString(b)
}

// gdSwitchParams 切换主动安全标准时迁移参数。调用方持有 t.mu
func (t *Terminal) gdSwitchParams(toGD bool) {
	if toGD {
		t.gdFill()
		return
	}
	for _, id := range []int{0xF364, 0xF365} {
		if g := gdParamIndex[id]; g != nil && t.Params[id] == g.Def {
			t.Params[id] = paramIndex[id].Def
		}
	}
	delete(t.Params, 0xF370)
}

// ---------------- 报警附加信息 ----------------

// gdAlarmFlag 报警标识号（表 5-16）：终端ID BYTE[30] + 时间 BCD[6] + 序号 + 附件数量 + 预留 BYTE[2] = 40 字节
func gdAlarmFlag(deviceID string, ts []byte, seq, files int) []byte {
	return cat(fixed(strings.ToUpper(deviceID), 30), ts, []byte{byte(seq), byte(files)}, make([]byte, 2))
}

// gdVehicleStatus 车辆状态 WORD（表 6-9）：bit0 ACC bit1 左转 bit2 右转 bit3 雨刮 bit4 制动 bit5 插卡 bit10 定位。调用方持有 t.mu
func (t *Terminal) gdVehicleStatus() int {
	st, ext := t.statusWord(), t.ExtSignal
	v := 0
	set := func(bit int, on bool) {
		if on {
			v |= 1 << bit
		}
	}
	set(0, st&1 != 0)
	set(1, ext&(1<<3) != 0) // 0x25 bit3 左转向灯
	set(2, ext&(1<<2) != 0) // 0x25 bit2 右转向灯
	set(4, ext&(1<<4) != 0) // 0x25 bit4 制动
	set(5, t.Driver.OnDuty)
	set(10, st&2 != 0)
	return v
}

// gdAttr 构造粤标报警附加信息（表 5-15/5-17/5-18/5-20）与报警标识号。flag<0 自动（事件 0，报警 1 开始）。调用方持有 t.mu
func (t *Terminal) gdAttr(attr, typ, level, files, flag int) ([]byte, []byte) {
	now := time.Now()
	ts := bcdTime(now)
	if key := now.Format("060102150405"); key != t.gdSeqKey {
		t.gdSeqKey, t.gdSeq = key, 0
	} else {
		t.gdSeq++
	}
	mark := gdAlarmFlag(t.Cfg.DeviceID, ts, t.gdSeq, files)
	id := u32(int(t.GD.AlarmID))
	t.GD.AlarmID++
	if flag < 0 {
		flag = 1
		if gdIsEvent(attr, typ) {
			flag = 0
		}
	}
	lat, lng := int(abs(t.Lat)*1e6), int(abs(t.Lng)*1e6)
	speed := min(int(t.speed), 250)
	tail := cat([]byte{byte(speed)}, u16(t.alt), u32(lat), u32(lng), ts, u16(t.gdVehicleStatus()), mark)
	var v []byte
	switch attr {
	case 0x64: // 标志 类型 级别 前车车速 前车/行人距离 偏离类型 道路标志识别类型 道路标志识别数据 + 车速..报警标识号
		front, dist, dev, sign, signData := 0, 0, 0, 0, 0
		if typ == 1 || typ == 2 { // 前车车速：仅 0x01 和 0x02 有效
			front = max(speed-10, 0)
		}
		if typ == 1 || typ == 2 || typ == 4 {
			dist = 15 // 1.5s
		}
		if typ == 2 {
			dev = 1 // 左侧偏离
		}
		if typ == 6 || typ == 0x10 {
			sign, signData = 1, 60 // 限速 60
		}
		v = cat(id, []byte{byte(flag), byte(typ), byte(level), byte(front), byte(dist), byte(dev), byte(sign), byte(signData)}, tail)
	case 0x65: // 标志 类型 级别 疲劳程度 预留 BYTE[4]
		fatigue := 0
		if typ == 1 {
			fatigue = 5
		}
		v = cat(id, []byte{byte(flag), byte(typ), byte(level), byte(fatigue)}, make([]byte, 4), tail)
	case 0x66: // 标志 + 车速..报警标识号 + 列表总数 + [位置 BYTE[2] 类型 WORD 胎压 胎温 电量]
		kpa, temp, batt := 830, 65, 80
		switch typ {
		case 2:
			kpa = 1050
		case 3:
			kpa = 560
		case 4:
			temp = 95
		case 6:
			kpa = 700
		case 7:
			kpa = 760
		case 8:
			batt = 8
		}
		v = cat(id, []byte{byte(flag)}, tail, []byte{1, 0, 0}, u16(1<<(typ-1)), u16(kpa), u16(temp), u16(batt))
	case 0x67: // 标志 类型
		v = cat(id, []byte{byte(flag), byte(typ)}, tail)
	}
	t.adasPending[string(mark)] = adasAlarm{attr, typ, level}
	return cat([]byte{byte(attr), byte(len(v))}, v), mark
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// gdExtraAttrs 0xF1 安装异常 / 0xF2 算法异常（DWORD，非 0 时附带）。调用方持有 t.mu
func (t *Terminal) gdExtraAttrs() []byte {
	if !t.Cfg.yue() {
		return nil
	}
	var b []byte
	if t.GD.InstallErr != 0 {
		b = cat(b, []byte{0xF1, 4}, u32(int(t.GD.InstallErr)))
	}
	if t.GD.AlgoErr != 0 {
		b = cat(b, []byte{0xF2, 4}, u32(int(t.GD.AlgoErr)))
	}
	return b
}

// ---------------- 附件 ----------------

// gdAttachFiles 粤标附件：<文件类型>_<通道号>_<报警类型>_<序号>_<报警编号>.<后缀>
// 通道号 64 ADAS / 65 DSM / 67 BSD，与通道无关填 0（TPMS、车辆状态数据记录文件）；报警类型 = 外设 ID + 报警类型（十六进制，如 6401）；
// 序号按 同通道同类型 分别从 0 编号。附件 ≥3 个时：图片… + 视频 mp4 + 车辆状态数据记录文件 bin（文件类型 03 文本）
func (t *Terminal) gdAttachFiles(a adasAlarm, total int, no string, rec []byte) []AttFile {
	ch := map[int]int{0x64: 64, 0x65: 65, 0x66: 0, 0x67: 67}[a.attr]
	code := fmt.Sprintf("%02X%02X", a.attr, a.typ)
	imgType := 0
	if a.attr == 0x65 && a.typ == 0x11 { // 驾驶员变更事件：面部特征图片
		imgType = 4
	}
	nImg, video, bin := total, false, false
	if total >= 2 {
		nImg, video = total-1, true
	}
	if total >= 3 {
		nImg, bin = total-2, true
	}
	var files []AttFile
	mch := mediaChOf(a.attr) // 媒体按报警类型分通道（TPMS 文件名通道号填 0，但媒体源仍按 66 取）
	// 同一次上报的图片共用同一画面（含烧入的时间水印），循环外取一次
	cur := t.attachmentImage(mch)
	if cur == nil {
		cur = t.stampIf(makeJPEG(max(ch, 1), 5, 3, time.Now()), mch)
	}
	for i := 0; i < nImg; i++ {
		files = append(files, AttFile{fmt.Sprintf("%02d_%d_%s_%d_%s.jpg", imgType, ch, code, i, no), imgType, cur})
	}
	if video {
		files = append(files, AttFile{fmt.Sprintf("02_%d_%s_0_%s.mp4", ch, code, no), 2, t.attachmentVideo(mch)})
	}
	if bin {
		files = append(files, AttFile{fmt.Sprintf("03_0_%s_0_%s.bin", code, no), 3, rec})
	}
	return files
}

const gdRecBlock = 64

// gdRecordFile 车辆状态数据记录文件（表 5-22）：n 个 64 字节数据块，报警前后各 n/2 秒、每秒一块。调用方持有 t.mu
func (t *Terminal) gdRecordFile(n int) []byte {
	alarm, status := t.alarmWord(), t.statusWord()
	lat, lng := int(abs(t.Lat)*1e6), int(abs(t.Lng)*1e6)
	base := time.Now().Add(-time.Duration(n/2) * time.Second)
	turn := 0
	if t.ExtSignal&(1<<3) != 0 {
		turn = 1
	} else if t.ExtSignal&(1<<2) != 0 {
		turn = 2
	}
	brake := int(t.ExtSignal >> 4 & 1)
	out := make([]byte, 0, n*gdRecBlock)
	for i := 0; i < n; i++ {
		sp := max(t.speed+float64(i%5)-2, 0)
		gear := 0
		if sp > 0 {
			gear = min(1+int(sp/15), 9)
		}
		s16 := func(v int) []byte { return u16(int(uint16(int16(v)))) }
		blk := cat(u32(n), u32(i+1), u32(int(alarm)), u32(int(status)), u32(lat), u32(lng), u16(t.alt), u16(int(sp*10)),
			u16(int(t.dir)%360), bcdTime(base.Add(time.Duration(i)*time.Second)),
			s16(i%7-3), s16(i%5-2), s16(100), s16(0), s16(0), s16(i%3-1), // 加速度 0.01g（Z 轴 1g），角速度 0.01°/s
			u16(int(sp*10)), u16(int(sp*10)), []byte{byte(gear), byte(min(10+i%20, 100)), byte(brake * 30), byte(brake)},
			u16(800+int(sp)*20), s16(0), []byte{byte(turn)}, make([]byte, 2))
		var sum byte
		for _, x := range blk {
			sum += x
		}
		out = append(out, append(blk, sum)...)
	}
	return out
}

// ---------------- 参数 ----------------

const gdProtoVersion = "1" // 0xF370 智能视频协议版本信息，初始版本 1

func gdCommon(volName string, shot []Opt, shotDef int64, shotHelp string) []SField {
	return []SField{
		b1("speed", "报警判断速度阈值", "km/h", 30, "0~60，车速高于此阈值才使能报警；0xFF 不修改"),
		b1("volume", volName, "", 6, "0~8，8 最大，0 静音；0xFF 不修改"),
		{Key: "shot", Name: "主动拍照策略", Size: 1, Opts: append(shot, Opt{0xFF, "不修改"})},
		{Key: "shotInt", Name: "主动定时拍照时间间隔", Size: 2, Unit: "秒", Def: shotDef, Help: shotHelp},
		{Key: "shotDist", Name: "主动定距拍照距离间隔", Size: 2, Unit: "米", Def: 200, Help: "0~60000，0 不抓拍，0xFFFF 不修改；策略为定距拍照时有效"},
		b1("shotCount", "单次主动拍照张数", "张", 3, "1~10；0xFF 不修改"),
		b1("shotGap", "单次主动拍照时间间隔", "100ms", 2, "1~5；0xFF 不修改"),
		{Key: "picRes", Name: "拍照分辨率", Size: 1, Opts: append(append([]Opt{}, optPicRes...), Opt{0xFF, "不修改"}), Def: 5, Help: "也适用于报警触发拍照"},
		{Key: "vidRes", Name: "视频录制分辨率", Size: 1, Opts: append(append([]Opt{}, optVidRes...), Opt{0xFF, "不修改"}), Def: 3, Help: "也适用于报警触发视频"},
	}
}

// gdGroup 某类报警的 分级速度阈值 / 前后视频录制时间 / 拍照张数 / 拍照间隔（单位 100ms）
func gdGroup(key, name string, gapHelp string) []SField {
	return []SField{
		b1(key+"Speed", name+"分级速度阈值", "km/h", 50, "0~220，触发时车速高于阈值为二级报警，否则一级；0xFF 不修改"),
		b1(key+"Video", name+"前后视频录制时间", "秒", 5, "0~60，0 不录像；0xFF 不修改"),
		b1(key+"Photos", name+"拍照张数", "张", 3, "0~10，0 不抓拍；0xFF 不修改"),
		b1(key+"PhotoInt", name+"拍照间隔", "100ms", 2, gapHelp+"；0xFF 不修改"),
	}
}

// 表 5-10 高级驾驶辅助系统参数（64 字节）
var gdAdasSchema = &Schema{Fields: func() []SField {
	fs := gdCommon("报警提示音量", []Opt{{0, "不开启"}, {1, "定时拍照"}, {2, "定距拍照"}, {3, "保留"}}, 60, "0~3600，0 不抓拍，0xFFFF 不修改；策略为定时拍照时有效")
	fs = append(fs,
		SField{Key: "alarmEn", Name: "报警使能", Size: 4, Def: 0x000F0FFF, Help: "0xFFFFFFFF 不修改", Bits: map[int]string{0: "障碍检测一级", 1: "障碍检测二级",
			2: "频繁变道一级", 3: "频繁变道二级", 4: "车道偏离一级", 5: "车道偏离二级", 6: "前向碰撞一级", 7: "前向碰撞二级",
			8: "行人碰撞一级", 9: "行人碰撞二级", 10: "车距过近一级", 11: "车距过近二级", 16: "道路标识超限",
			17: "实线变道一级", 18: "实线变道二级", 19: "车厢过道行人检测"}},
		SField{Key: "eventEn", Name: "事件使能", Size: 4, Def: 3, Help: "0xFFFFFFFF 不修改", Bits: map[int]string{0: "道路标识识别", 1: "主动拍照"}},
		b1("rsv1", "预留字段", "", 0),
		b1("obstDist", "障碍物报警距离阈值", "100ms", 30, "10~50；0xFF 不修改"))
	fs = append(fs, gdGroup("obst", "障碍物报警", "1~10")...)
	fs = append(fs, b1("lcTime", "频繁变道报警判断时间段", "秒", 60, "30~120；0xFF 不修改"), b1("lcCount", "频繁变道报警判断次数", "次", 5, "3~10；0xFF 不修改"))
	fs = append(fs, gdGroup("lc", "频繁变道报警", "1~10")...)
	fs = append(fs, gdGroup("ldw", "车道偏离报警", "1~10")...)
	fs = append(fs, b1("fcwTime", "前向碰撞报警时间阈值", "100ms", 27, "10~50，目前使用国标规定值 27；0xFF 不修改"))
	fs = append(fs, gdGroup("fcw", "前向碰撞报警", "1~10")...)
	fs = append(fs, b1("pcwTime", "行人碰撞报警时间阈值", "100ms", 30, "10~50；0xFF 不修改"))
	fs = append(fs, gdGroup("pcw", "行人碰撞报警", "1~10")...)
	fs[len(fs)-4].Name, fs[len(fs)-4].Help = "行人碰撞报警使能速度阈值", "0~220，低于该值时报警，高于该值时功能关闭；0xFF 不修改"
	fs = append(fs, b1("hmwDist", "车距过近报警距离阈值", "100ms", 10, "10~50；0xFF 不修改"))
	fs = append(fs, gdGroup("hmw", "车距过近报警", "1~10")...)
	fs = append(fs, b1("tsrPhotos", "道路标志识别拍照张数", "张", 1, "0~10，0 不抓拍；0xFF 不修改"), b1("tsrPhotoInt", "道路标志识别拍照间隔", "100ms", 2, "1~10；0xFF 不修改"))
	fs = append(fs, gdGroup("sld", "实线变道报警", "1~10")...)
	fs = append(fs, gdGroup("aisle", "车厢过道行人检测报警", "1~10")...)
	return append(fs, SField{Key: "rsv2", Name: "保留字段", Size: 4, Kind: "hex"})
}()}

// 表 5-11 驾驶员状态监测系统参数（66 字节）
var gdDsmSchema = &Schema{Fields: func() []SField {
	fs := gdCommon("报警音量", []Opt{{0, "不开启"}, {1, "定时拍照"}, {2, "定距拍照"}, {3, "插卡触发"}, {4, "保留"}}, 3600, "60~60000，0xFFFF 不修改；策略为定时拍照时有效")
	fs = append(fs,
		SField{Key: "alarmEn", Name: "报警使能", Size: 4, Def: 0x000FFFFF, Help: "0xFFFFFFFF 不修改", Bits: map[int]string{0: "疲劳驾驶一级", 1: "疲劳驾驶二级",
			2: "接打手持电话一级", 3: "接打手持电话二级", 4: "抽烟一级", 5: "抽烟二级", 6: "不目视前方一级", 7: "不目视前方二级",
			8: "驾驶员异常一级", 9: "驾驶员异常二级", 10: "摄像机遮挡一级", 11: "摄像机遮挡二级", 12: "不系安全带一级", 13: "不系安全带二级",
			14: "红外墨镜阻断失效一级", 15: "红外墨镜阻断失效二级", 16: "双脱把一级", 17: "双脱把二级", 18: "玩手机一级", 19: "玩手机二级"}},
		SField{Key: "eventEn", Name: "事件使能", Size: 4, Def: 3, Help: "0xFFFFFFFF 不修改", Bits: map[int]string{0: "驾驶员更换事件", 1: "主动拍照事件"}},
		SField{Key: "smokeInt", Name: "吸烟报警判断时间间隔", Size: 2, Unit: "秒", Def: 300, Help: "0~300，间隔内仅触发一次；0xFFFF 不修改"},
		SField{Key: "phoneInt", Name: "接打手持电话报警判断时间间隔", Size: 2, Unit: "秒", Def: 300, Help: "0~300，间隔内仅触发一次；0xFFFF 不修改"},
		SField{Key: "rsv1", Name: "预留字段", Size: 3, Kind: "hex"})
	fs = append(fs, gdGroup("fatigue", "疲劳驾驶报警", "1~5")...)
	fs = append(fs, gdGroup("phone", "接打手持电话报警", "1~5")...)
	fs = append(fs, gdGroup("smoke", "抽烟报警", "1~5")...)
	fs = append(fs, gdGroup("distract", "不目视前方报警", "1~5")...)
	fs = append(fs, gdGroup("abnormal", "驾驶行为异常", "1~5")...)
	fs = append(fs, SField{Key: "idTrigger", Name: "驾驶员身份识别触发方式", Size: 1, Def: 1,
		Opts: []Opt{{0, "不开启"}, {1, "定时触发"}, {2, "定距触发"}, {3, "插卡开始行驶触发"}, {4, "保留"}, {0xFF, "不修改"}}})
	fs = append(fs, b1("coverSpeed", "摄像机遮挡报警分级速度阈值", "km/h", 50, "0~220；0xFF 不修改"))
	fs = append(fs, gdGroup("belt", "不系安全带报警", "1~5")...)
	fs = append(fs, gdGroup("ir", "红外墨镜阻断失效报警", "1~5")...)
	fs = append(fs, gdGroup("hands", "双脱把报警", "1~5")...)
	fs = append(fs, gdGroup("play", "玩手机报警", "1~5")...)
	return append(fs, SField{Key: "rsv2", Name: "保留字段", Size: 2, Kind: "hex"})
}()}

// 表 5-12 胎压监测系统参数（36 字节，布局与苏标相同；数值 0xFFFF 表示不修改）
var gdTpmsSchema = &Schema{Fields: func() []SField {
	fs := append([]SField{}, tpmsSchema.Fields...)
	fs[1].Opts = append(append([]Opt{}, fs[1].Opts...), Opt{0xFFFF, "不修改"})
	return fs
}()}

// 表 5-13 盲区监测系统参数（2 字节）
var gdBsdSchema = &Schema{Fields: []SField{
	{Key: "rear", Name: "后方接近报警时间阈值", Size: 1, Unit: "秒", Def: 5, Help: "1~10；0xFF 不修改"},
	{Key: "side", Name: "侧后方接近报警时间阈值", Size: 1, Unit: "秒", Def: 5, Help: "1~10；0xFF 不修改"}}}

var gdParamDefs = []ParamDef{
	p(0xF364, "高级驾驶辅助系统参数（ADAS）", "BYTES", "主动安全", schemaDefault(gdAdasSchema), std("粤标 表5-10"), schema(gdAdasSchema),
		hint("BYTE[64]；平台 8103 下发时数值字段为 0xFF/0xFFFF/0xFFFFFFFF 表示不修改，终端保留原值")),
	p(0xF365, "驾驶员状态监测系统参数（DSM）", "BYTES", "主动安全", schemaDefault(gdDsmSchema), std("粤标 表5-11"), schema(gdDsmSchema),
		hint("BYTE[66]；数值字段 0xFF/0xFFFF 表示不修改")),
	p(0xF366, "胎压监测系统参数（TPMS）", "BYTES", "主动安全", schemaDefault(gdTpmsSchema), std("粤标 表5-12"), schema(gdTpmsSchema),
		hint("BYTE[36]；数值字段 0xFFFF 表示不修改")),
	p(0xF367, "盲区监测系统参数（BSD）", "BYTES", "主动安全", schemaDefault(gdBsdSchema), std("粤标 表5-13"), schema(gdBsdSchema),
		hint("BYTE[2]；0xFF 表示不修改")),
	p(0xF370, "智能视频协议版本信息", "BYTE", "主动安全", gdProtoVersion, std("粤标 表5-9"), readOnly(),
		hint("初始版本 1，每次修订递增；只支持获取（8104/8106），8103 设置被忽略")),
}

var gdParamIndex = func() map[int]*ParamDef {
	m := map[int]*ParamDef{}
	for i := range gdParamDefs {
		m[gdParamDefs[i].ID] = &gdParamDefs[i]
	}
	return m
}()

// pdefStd 按主动安全标准取参数定义（粤标覆盖 F364~F367 并增加 F370）
func pdefStd(yue bool, id int) *ParamDef {
	if yue {
		if d := gdParamIndex[id]; d != nil {
			return d
		}
	}
	return pdef(id)
}

// gdMerge 8103 下发粤标结构参数：数值字段全为 0xFF 表示不修改，保留原值；返回合并后的 hex
func gdMerge(id int, old string, nv []byte) string {
	d := gdParamIndex[id]
	if d == nil || d.Schema == nil {
		return hex.EncodeToString(nv)
	}
	ob, err := hex.DecodeString(gdNormalize(id, old))
	if err != nil {
		ob, _ = hex.DecodeString(d.Def)
	}
	out := append([]byte{}, ob...)
	off := 0
	for _, f := range d.Schema.Fields {
		end := off + f.Size
		if end > len(nv) {
			break
		}
		seg := nv[off:end]
		keep := f.Kind == "" // 数值字段全 0xFF = 不修改；字符串/保留字段照常覆盖
		for _, x := range seg {
			keep = keep && x == 0xFF
		}
		if !keep && end <= len(out) {
			copy(out[off:end], seg)
		}
		off = end
	}
	if len(nv) > off && len(nv) > len(out) { // 超出定义的扩展字节原样保留
		out = append(out[:off], nv[off:]...)
	}
	return hex.EncodeToString(out)
}

// ---------------- 0x8900 / 0x0900 透传 ----------------

const (
	gdPassStatus = 0xF7 // 外设状态信息
	gdPassInfo   = 0xF8 // 外设系统信息
)

func shortStr(s string) []byte { // 长度 BYTE（0~32）+ ASCII
	b := []byte(s)
	if len(b) > 32 {
		b = b[:32]
	}
	return append([]byte{byte(len(b))}, b...)
}

// gdPassBody 0x0900 上传基本信息（表 5-2 / 5-4）：透传类型 + 消息列表总数 + [外设ID 消息长度 消息内容]…。调用方持有 t.mu
func (t *Terminal) gdPassBody(typ int, ids []int) []byte {
	if len(ids) == 0 {
		ids = []int{0x64, 0x65, 0x66, 0x67}
	}
	var items []byte
	n := 0
	for _, id := range ids {
		p := t.periph(id)
		if p == nil {
			continue
		}
		var c []byte
		if typ == gdPassStatus { // 表 5-6：工作状态 BYTE + 报警状态 DWORD
			c = cat([]byte{byte(p.Work)}, u32(int(p.Alarm)))
		} else { // 表 5-7
			c = cat(shortStr(p.Company), shortStr(p.Model), shortStr(p.HwVer), shortStr(p.SwVer), shortStr(p.DevID), shortStr(p.Customer))
		}
		items = cat(items, []byte{byte(id), byte(len(c))}, c)
		n++
	}
	return cat([]byte{byte(typ), byte(n)}, items)
}

// gdH8900 粤标 8900 查询基本信息（表 5-1）：透传类型 + 外设 ID 列表总数 + 外设 ID…；回 0001 后以 0900 上传
func (t *Terminal) gdH8900(m *Msg) bool {
	r := newReader(m.Body)
	typ := r.u8()
	if typ != gdPassStatus && typ != gdPassInfo {
		return false
	}
	n := r.u8()
	var ids []int
	for i := 0; i < n && r.err == nil; i++ {
		ids = append(ids, r.u8())
	}
	if r.err != nil && len(m.Body) > 1 {
		t.ack(m, 2)
		return true
	}
	t.ack(m, 0)
	t.mu.Lock()
	body := t.gdPassBody(typ, ids)
	t.mu.Unlock()
	name := map[int]string{gdPassStatus: "外设状态信息 F7", gdPassInfo: "外设系统信息 F8"}[typ]
	t.addInbox("passthrough", "查询基本信息（粤标 8900）", fmt.Sprintf("%s 外设 %v", name, hexIDs(ids)), 0, nil)
	t.send(0x0900, body, fmt.Sprintf("上传基本信息 %s %d 个外设", name, body[1]))
	return true
}

func hexIDs(ids []int) string {
	if len(ids) == 0 {
		return "全部"
	}
	var s []string
	for _, id := range ids {
		s = append(s, fmt.Sprintf("0x%02X", id))
	}
	return strings.Join(s, ",")
}

// ---------------- 8105 无线升级 + 0x1FC4 升级进度 + 0x0108 结果 ----------------

// gdUpgradeFile 升级文件命名：<设备类型>_<厂家编号>_<设备型号>_<依赖软件版本号>_<软件版本号>.<后缀名>
// 设备类型 01 终端 03 ADAS 04 DSM 05 BSD 06 TPMS → 0108/1FC4 升级类型 0x00 / 0x64 / 0x65 / 0x67 / 0x66
type gdUpgradeFile struct {
	Type          int
	Maker, Model  string
	Depend, SwVer string
	OK            bool
}

func parseGDUpgradeName(rawURL string) gdUpgradeFile {
	name := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		name = u.Path
	}
	name = path.Base(name)
	if i := strings.LastIndex(name, "."); i > 0 {
		name = name[:i]
	}
	f := strings.Split(name, "_")
	if len(f) != 5 {
		return gdUpgradeFile{}
	}
	typ, ok := map[string]int{"01": 0x00, "03": 0x64, "04": 0x65, "05": 0x67, "06": 0x66}[f[0]]
	return gdUpgradeFile{typ, f[1], f[2], f[3], f[4], ok}
}

var gdUpgradeStatus = map[int]string{1: "固件下载中", 2: "固件下载成功", 3: "固件下载失败", 4: "固件安装中", 5: "安装成功", 6: "安装失败",
	7: "未找到目标设备", 8: "硬件型号不支持", 9: "软件版本相同", 0x0a: "软件版本不支持", 0x0b: "其他"}
var gdUpgradeResult = map[int]string{0: "成功", 1: "失败", 2: "取消", 0x10: "未找到目标设备", 0x11: "硬件型号不支持", 0x12: "软件版本相同", 0x13: "软件版本不支持"}
var gdUpgradeTypeName = map[int]string{0: "终端", 0x0C: "道路运输证IC卡读卡器", 0x34: "北斗定位模块", 0x64: "ADAS", 0x65: "DSM", 0x66: "TPMS", 0x67: "BSD"}

// 模拟结果（页面可选）：空 自动 | dlfail 下载失败 | installfail 安装失败 | cancel 取消 | nodevice 未找到设备 | hw 硬件不支持 | same 版本相同 | unsupported 版本不支持
var gdUpgradeSims = map[string]string{"": "自动（成功；版本相同时回版本相同）", "dlfail": "固件下载失败", "installfail": "安装失败", "cancel": "取消",
	"nodevice": "未找到目标设备", "hw": "硬件型号不支持", "same": "软件版本相同", "unsupported": "软件版本不支持"}

var gdStep = 400 * time.Millisecond

// gdUpgrade 8105 命令字 1（表 5-30/5-31）：按升级文件名确定升级对象，逐步上报 0x1FC4 进度，最后 0x0108 结果
func (t *Terminal) gdUpgrade(serial int, rawURL, hw, fw string) {
	f := parseGDUpgradeName(rawURL)
	typ, ver := f.Type, fw
	if ver == "" {
		ver = f.SwVer
	}
	t.mu.Lock()
	sim := t.GD.UpgradeSim
	t.GD.UpgradeSim = "" // 只作用一次
	cur := t.Cfg.FwVer
	work := 1
	if p := t.periph(typ); p != nil {
		cur, work = p.SwVer, p.Work
	}
	t.markDirty()
	t.mu.Unlock()
	tn := gdUpgradeTypeName[typ]
	t.logf("[粤标升级] 对象=%s(0x%02X) 文件名解析=%v 目标版本=%s 当前版本=%s 硬件=%s", tn, typ, f.OK, ver, cur, hw)
	progress := func(st, pct, code int) {
		t.send(0x1FC4, cat(u16(serial), []byte{byte(typ), byte(st), byte(pct), byte(code)}),
			fmt.Sprintf("升级进度 %s %s %d%%", tn, gdUpgradeStatus[st], pct))
		time.Sleep(gdStep)
	}
	result := func(r int) {
		t.send(0x0108, []byte{byte(typ), byte(r)}, fmt.Sprintf("升级结果 %s %s", tn, gdUpgradeResult[r]))
		t.addInbox("upgrade", "粤标升级结果", fmt.Sprintf("%s：%s（目标版本 %s）", tn, gdUpgradeResult[r], ver), 0, nil)
	}
	switch {
	case sim == "nodevice" || work == 0x10:
		progress(7, 0, 0)
		result(0x10)
		return
	case sim == "hw":
		progress(8, 0, 0)
		result(0x11)
		return
	case sim == "same" || ver != "" && ver == cur:
		progress(9, 0, 0)
		result(0x12)
		return
	case sim == "unsupported":
		progress(0x0a, 0, 0)
		result(0x13)
		return
	}
	// 下载：http(s) 实际下载（上限 50MB，按 Content-Length 报进度），其它地址模拟
	dlErr := ""
	if (strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://")) && sim == "" {
		dlErr = t.gdDownload(rawURL, func(pct int) { progress(1, pct, 0) })
	} else {
		for pct := 0; pct <= 100; pct += 25 {
			if sim == "dlfail" && pct == 50 || sim == "cancel" && pct == 75 {
				break
			}
			progress(1, pct, 0)
		}
		if sim == "dlfail" {
			dlErr = "模拟下载失败"
		}
	}
	if sim == "cancel" {
		progress(0x0b, 75, 0)
		result(2)
		return
	}
	if dlErr != "" {
		t.errf("[粤标升级] %s", dlErr)
		progress(3, 0, 1)
		result(1)
		return
	}
	progress(2, 100, 0)
	for pct := 0; pct <= 100; pct += 50 {
		if sim == "installfail" && pct == 100 {
			break
		}
		progress(4, pct, 0)
	}
	if sim == "installfail" {
		progress(6, 50, 2)
		result(1)
		return
	}
	progress(5, 100, 0)
	if ver != "" {
		t.mu.Lock()
		if p := t.periph(typ); p != nil {
			p.SwVer = ver
		} else if typ == 0 {
			t.Cfg.FwVer = ver
		}
		t.markDirty()
		t.mu.Unlock()
	}
	result(0)
}

func (t *Terminal) gdDownload(rawURL string, report func(int)) string {
	c := http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get(rawURL)
	if err != nil {
		return "升级包下载失败: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Sprintf("升级包下载失败: HTTP %d", resp.StatusCode)
	}
	total := resp.ContentLength
	report(0)
	var got int64
	next := int64(25)
	buf := make([]byte, 32<<10)
	for got < 50<<20 {
		n, err := resp.Body.Read(buf)
		got += int64(n)
		if total > 0 && got*100/total >= next && next < 100 {
			report(int(got * 100 / total))
			next = (got*100/total)/25*25 + 25
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "升级包下载失败: " + err.Error()
		}
	}
	report(100)
	t.logf("[粤标升级] 升级包下载完成 %d 字节", got)
	return ""
}

// ---------------- 8801 外设立即拍照（表 5-34） ----------------

// gdH8801 通道 0x64 控制 ADAS 拍照 / 0x65 控制 DSM 拍照：张数取 F364/F365 单次主动拍照张数（其余字段仅主机拍照有效）
func (t *Terminal) gdH8801(m *Msg, ch int) {
	pid := map[int]int{0x64: 0xF364, 0x65: 0xF365}[ch]
	t.mu.Lock()
	n := 3
	if b, err := hex.DecodeString(t.Params[pid]); err == nil && len(b) > 7 && b[7] > 0 && b[7] != 0xFF {
		n = int(b[7])
	}
	res := 5
	if b, err := hex.DecodeString(t.Params[pid]); err == nil && len(b) > 9 && b[9] >= 1 && b[9] <= 6 {
		res = int(b[9])
	}
	var items []MediaItem
	for i := 0; i < min(n, 5); i++ {
		items = append(items, t.newMedia(0, ch, 0, res, 3, 0))
	}
	t.mu.Unlock()
	body := cat(u16(m.Serial), []byte{0}, u16(len(items)))
	for _, it := range items {
		body = cat(body, u32(it.ID))
	}
	t.send(0x0805, body, fmt.Sprintf("外设 %s 立即拍照 %d 张", gdPeriphNames[ch], len(items)))
	t.addInbox("media", "外设立即拍照（粤标 8801）", fmt.Sprintf("%s（通道 0x%02X）%d 张", gdPeriphNames[ch], ch, len(items)), 0, nil)
	for i, it := range items {
		if i > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		if err := t.uploadMedia(it); err != nil {
			t.errf("多媒体 %d 上传失败: %v", it.ID, err)
			return
		}
	}
}

// avChannelOK 实时/历史音视频通道：1~4；粤标另支持智能通道 0x64 ADAS / 0x65 DSM / 0x67 BSD（表 5-35）
func (t *Terminal) avChannelOK(ch int) bool {
	if ch >= 1 && ch <= avChannels {
		return true
	}
	t.mu.Lock()
	yue := t.Cfg.yue()
	t.mu.Unlock()
	return yue && (ch == 0x64 || ch == 0x65 || ch == 0x67)
}

// ---------------- 页面动作 ----------------

// gdAction 粤标页面动作：gdPeriph 修改外设状态/信息，gdReport 主动上传 0900，gdSet 异常信息/升级模拟
func (t *Terminal) gdAction(typ string, p map[string]any) error {
	switch typ {
	case "gdPeriph":
		id := pint(p, "id", 0)
		t.mu.Lock()
		defer t.mu.Unlock()
		pp := t.periph(id)
		if pp == nil {
			return fmt.Errorf("外设 ID 只能是 0x64~0x67")
		}
		if _, ok := p["work"]; ok {
			w := pint(p, "work", 1)
			if gdWorkNames[w] == "" {
				return fmt.Errorf("工作状态无效")
			}
			pp.Work = w
		}
		if _, ok := p["alarm"]; ok {
			pp.Alarm = uint32(pint(p, "alarm", 0))
		}
		for k, dst := range map[string]*string{"company": &pp.Company, "model": &pp.Model, "hwVer": &pp.HwVer, "swVer": &pp.SwVer, "devId": &pp.DevID, "customer": &pp.Customer} {
			if v, ok := p[k]; ok {
				s := strings.TrimSpace(fmt.Sprint(v))
				if len(s) > 32 {
					return fmt.Errorf("%s 最多 32 个字符", k)
				}
				*dst = s
			}
		}
		t.markDirty()
		return nil
	case "gdReport":
		pt := pint(p, "passType", gdPassStatus)
		if pt != gdPassStatus && pt != gdPassInfo {
			return fmt.Errorf("type 只能是 0xF7/0xF8")
		}
		var ids []int
		if id := pint(p, "id", 0); id != 0 {
			ids = []int{id}
		}
		t.mu.Lock()
		body := t.gdPassBody(pt, ids)
		t.mu.Unlock()
		_, _, err := t.send(0x0900, body, fmt.Sprintf("上传基本信息 0x%02X %d 个外设", pt, body[1]))
		return err
	case "gdSet":
		t.mu.Lock()
		defer t.mu.Unlock()
		if _, ok := p["installErr"]; ok {
			t.GD.InstallErr = uint32(pint(p, "installErr", 0))
		}
		if _, ok := p["algoErr"]; ok {
			t.GD.AlgoErr = uint32(pint(p, "algoErr", 0))
		}
		if v, ok := p["upgradeSim"]; ok {
			s := fmt.Sprint(v)
			if _, ok := gdUpgradeSims[s]; !ok {
				return fmt.Errorf("upgradeSim 无效")
			}
			t.GD.UpgradeSim = s
		}
		t.markDirty()
		return nil
	}
	return fmt.Errorf("未知动作 %s", typ)
}

// safetyView 页面：当前标准、报警类型表、粤标外设状态。调用方持有 t.mu
func (t *Terminal) safetyView() map[string]any {
	std := "jsatl"
	if t.Cfg.yue() {
		std = StdGD
	}
	return map[string]any{"std": std, "name": t.Cfg.stdName(), "types": t.Cfg.typeNames(), "gd": t.GD,
		"workNames": gdWorkNames, "alarmBits": gdPeriphAlarmBits, "upgradeSims": gdUpgradeSims, "verForced": t.Cfg.yue() && t.Cfg.Version == "2013"}
}
