package main

// 终端参数表：JT/T 808-2013/2019 表 13 + JT/T 1078 表 2 + JSATL12 主动安全参数。
// 存储格式（state/<phone>.json 的 params）：DWORD/WORD/BYTE 为十进制串，STRING 为文本，BYTES 为 hex。
// 结构体参数（BYTES 或按位组合的 DWORD）带 Schema，Web 页面逐字段编辑，编码结果仍按上述格式存储，与旧状态文件兼容。

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Opt struct {
	V int64  `json:"v"`
	L string `json:"l"`
}

type ParamDef struct {
	ID       int            `json:"-"`
	Name     string         `json:"name"`
	Type     string         `json:"type"` // DWORD / WORD / BYTE / STRING / BYTES
	Group    string         `json:"group"`
	Def      string         `json:"def"`
	Unit     string         `json:"unit,omitempty"`
	Help     string         `json:"help,omitempty"`
	Min      *int64         `json:"min,omitempty"`
	Max      *int64         `json:"max,omitempty"`
	Opts     []Opt          `json:"opts,omitempty"`     // 枚举值
	Bits     map[int]string `json:"bits,omitempty"`     // 按位开关（报警屏蔽字等）
	Schema   *Schema        `json:"schema,omitempty"`   // 结构体 / 位段
	Std      string         `json:"std,omitempty"`      // 来源标准说明（如 "2019"、"JT/T 1078"、"JSATL12"）
	MaxLen   int            `json:"maxLen,omitempty"`   // STRING 最大字节数（GBK）
	ReadOnly bool           `json:"readOnly,omitempty"` // 只支持查询（如粤标 0xF370）
}

type po func(*ParamDef)

func unit(u string) po           { return func(d *ParamDef) { d.Unit = u } }
func hint(h string) po           { return func(d *ParamDef) { d.Help = h } }
func std(s string) po            { return func(d *ParamDef) { d.Std = s } }
func opts(o ...Opt) po           { return func(d *ParamDef) { d.Opts = o } }
func bitsOf(m map[int]string) po { return func(d *ParamDef) { d.Bits = m } }
func schema(s *Schema) po        { return func(d *ParamDef) { d.Schema = s } }
func readOnly() po               { return func(d *ParamDef) { d.ReadOnly = true } }
func rng(lo, hi int64) po {
	return func(d *ParamDef) { d.Min, d.Max = &lo, &hi }
}

func p(id int, name, typ, group, def string, o ...po) ParamDef {
	d := ParamDef{ID: id, Name: name, Type: typ, Group: group, Def: def}
	for _, f := range o {
		f(&d)
	}
	return d
}

var (
	optStrategy = opts(Opt{0, "定时汇报"}, Opt{1, "定距汇报"}, Opt{2, "定时和定距汇报"})
	optScheme   = opts(Opt{0, "根据 ACC 状态"}, Opt{1, "根据登录状态和 ACC 状态"})
	optAnswer   = opts(Opt{0, "自动接听"}, Opt{1, "ACC ON 时自动接听，OFF 时手动接听"})
	optPlate    = opts(Opt{0, "未上牌"}, Opt{1, "蓝色"}, Opt{2, "黄色"}, Opt{3, "黑色"}, Opt{4, "白色"}, Opt{5, "绿色"}, Opt{9, "其他"})
	gnssBits    = map[int]string{0: "GPS", 1: "北斗", 2: "GLONASS", 3: "Galileo"}
	chBits5     = map[int]string{0: "通道1", 1: "通道2", 2: "通道3", 3: "通道4", 4: "通道5"}
)

var paramDefs = []ParamDef{
	p(0x0001, "终端心跳发送间隔", "DWORD", "通信", "30", unit("秒"), rng(1, 3600), hint("无其它上行时按此间隔发送 0002 心跳")),
	p(0x0002, "TCP 消息应答超时时间", "DWORD", "通信", "10", unit("秒"), rng(1, 600), hint("T0。第 n 次重传超时 T(n+1)=T(n)×(n+1)：T0、T0、2T0、6T0…")),
	p(0x0003, "TCP 消息重传次数", "DWORD", "通信", "3", unit("次"), rng(0, 10)),
	p(0x0004, "UDP 消息应答超时时间", "DWORD", "通信", "10", unit("秒"), hint("终端配置中传输方式为 UDP 时使用（与 0x0005 一起决定 UDP 超时重传）")),
	p(0x0005, "UDP 消息重传次数", "DWORD", "通信", "3", unit("次")),
	p(0x0006, "SMS 消息应答超时时间", "DWORD", "通信", "30", unit("秒"), hint("仅保存")),
	p(0x0007, "SMS 消息重传次数", "DWORD", "通信", "3", unit("次")),

	p(0x0010, "主服务器 APN", "STRING", "服务器", "CMNET", hint("无线通信拨号访问点；CDMA 时为 PPP 拨号号码")),
	p(0x0011, "主服务器无线通信拨号用户名", "STRING", "服务器", ""),
	p(0x0012, "主服务器无线通信拨号密码", "STRING", "服务器", ""),
	p(0x0013, "主服务器地址", "STRING", "服务器", "", hint("IP 或域名；2019 版以冒号分隔主机和端口，多个服务器用分号分隔")),
	p(0x0014, "备份服务器 APN", "STRING", "服务器", "CMNET"),
	p(0x0015, "备份服务器无线通信拨号用户名", "STRING", "服务器", ""),
	p(0x0016, "备份服务器无线通信拨号密码", "STRING", "服务器", ""),
	p(0x0017, "备份服务器地址", "STRING", "服务器", "", hint("主服务器连续失败 3 次后交替尝试。格式 host 或 host:port，多个用分号分隔（取第一个）")),
	p(0x0018, "服务器 TCP 端口", "DWORD", "服务器", "0", rng(0, 65535), hint("2013 版参数；2019 版改为保留，端口写在地址里。连接备份服务器且地址未带端口时使用")),
	p(0x0019, "服务器 UDP 端口", "DWORD", "服务器", "0", rng(0, 65535), hint("2013 版参数，2019 版保留")),
	p(0x001A, "道路运输证 IC 卡认证主服务器", "STRING", "服务器", "", hint("IP 地址或域名")),
	p(0x001B, "IC 卡认证主服务器 TCP 端口", "DWORD", "服务器", "0", rng(0, 65535)),
	p(0x001C, "IC 卡认证主服务器 UDP 端口", "DWORD", "服务器", "0", rng(0, 65535)),
	p(0x001D, "IC 卡认证备份服务器", "STRING", "服务器", "", hint("IP 地址或域名，端口同主服务器")),
	p(0x0023, "从服务器 APN", "STRING", "服务器", "", std("2019"), hint("为空时终端使用主服务器相同配置")),
	p(0x0024, "从服务器无线通信拨号用户名", "STRING", "服务器", "", std("2019"), hint("为空时终端使用主服务器相同配置")),
	p(0x0025, "从服务器无线通信拨号密码", "STRING", "服务器", "", std("2019"), hint("为空时终端使用主服务器相同配置")),
	p(0x0026, "从服务器备份地址", "STRING", "服务器", "", std("2019"), hint("IP 或域名，主机和端口用冒号分割，多个服务器使用分号分割")),

	p(0x0020, "位置汇报策略", "DWORD", "汇报", "0", optStrategy),
	p(0x0021, "位置汇报方案", "DWORD", "汇报", "0", optScheme, hint("1：先判断登录状态（驾驶员插卡），已登录再根据 ACC 状态")),
	p(0x0022, "驾驶员未登录汇报时间间隔", "DWORD", "汇报", "30", unit("秒"), rng(1, 86400)),
	p(0x0027, "休眠时汇报时间间隔", "DWORD", "汇报", "60", unit("秒"), rng(1, 86400), hint("ACC 关时使用")),
	p(0x0028, "紧急报警时汇报时间间隔", "DWORD", "汇报", "5", unit("秒"), rng(1, 86400)),
	p(0x0029, "缺省时间汇报间隔", "DWORD", "汇报", "10", unit("秒"), rng(1, 86400)),
	p(0x002C, "缺省距离汇报间隔", "DWORD", "汇报", "200", unit("米"), rng(1, 100000)),
	p(0x002D, "驾驶员未登录汇报距离间隔", "DWORD", "汇报", "200", unit("米"), rng(1, 100000)),
	p(0x002E, "休眠时汇报距离间隔", "DWORD", "汇报", "1000", unit("米"), rng(1, 100000)),
	p(0x002F, "紧急报警时汇报距离间隔", "DWORD", "汇报", "50", unit("米"), rng(1, 100000)),
	p(0x0030, "拐点补传角度", "DWORD", "汇报", "30", unit("度"), rng(0, 179), hint("方向变化超过该角度立即补报一条位置；值小于 180，0 关闭")),
	p(0x0031, "电子围栏半径（非法位移阈值）", "WORD", "汇报", "500", unit("米")),

	p(0x0040, "监控平台电话号码", "STRING", "电话", ""),
	p(0x0041, "复位电话号码", "STRING", "电话", "", hint("可用此号码拨打终端让终端复位")),
	p(0x0042, "恢复出厂设置电话号码", "STRING", "电话", ""),
	p(0x0043, "监控平台 SMS 电话号码", "STRING", "电话", ""),
	p(0x0044, "接收终端 SMS 文本报警号码", "STRING", "电话", ""),
	p(0x0045, "终端电话接听策略", "DWORD", "电话", "0", optAnswer),
	p(0x0046, "每次最长通话时间", "DWORD", "电话", "4294967295", unit("秒"), hint("0 不允许通话，4294967295（0xFFFFFFFF）不限制")),
	p(0x0047, "当月最长通话时间", "DWORD", "电话", "4294967295", unit("秒"), hint("0 不允许通话，4294967295（0xFFFFFFFF）不限制")),
	p(0x0048, "监听电话号码", "STRING", "电话", ""),
	p(0x0049, "监管平台特权短信号码", "STRING", "电话", ""),

	p(0x0050, "报警屏蔽字", "DWORD", "报警", "0", bitsOf(alarmNames), hint("与 0200 报警标志对应，相应位为 1 则该报警被屏蔽（不上报、不触发拍照）")),
	p(0x0051, "报警发送文本 SMS 开关", "DWORD", "报警", "0", bitsOf(alarmNames), hint("相应位为 1 则该报警时发送文本 SMS（模拟器仅保存）")),
	p(0x0052, "报警拍摄开关", "DWORD", "报警", "1", bitsOf(alarmNames), hint("相应位为 1 则该报警时摄像头拍摄（模拟器支持紧急报警、碰撞侧翻）")),
	p(0x0053, "报警拍摄存储标志", "DWORD", "报警", "0", bitsOf(alarmNames), hint("相应位为 1 则对该报警时拍的照片存储，否则实时上传")),
	p(0x0054, "关键标志", "DWORD", "报警", "1", bitsOf(alarmNames), hint("相应位为 1 则该报警为关键报警（模拟器仅保存）")),
	p(0x0055, "最高速度", "DWORD", "报警", "100", unit("km/h"), hint("超过并持续 0x0056 秒产生超速报警；0 关闭")),
	p(0x0056, "超速持续时间", "DWORD", "报警", "10", unit("秒")),
	p(0x0057, "连续驾驶时间门限", "DWORD", "报警", "14400", unit("秒"), hint("超过产生疲劳驾驶报警；0 关闭")),
	p(0x0058, "当天累计驾驶时间门限", "DWORD", "报警", "28800", unit("秒")),
	p(0x0059, "最小休息时间", "DWORD", "报警", "1200", unit("秒"), hint("停车超过该时间连续驾驶时间清零")),
	p(0x005A, "最长停车时间", "DWORD", "报警", "3600", unit("秒"), hint("超过产生超时停车报警；0 关闭")),
	p(0x005B, "超速预警差值", "WORD", "报警", "50", unit("1/10 km/h")),
	p(0x005C, "疲劳驾驶预警差值", "WORD", "报警", "1800", unit("秒")),
	p(0x005D, "碰撞报警参数设置", "WORD", "报警", "4010", schema(&Schema{Fields: []SField{
		{Key: "w", Size: 2, Subs: []SSub{
			{Key: "time", Name: "碰撞时间", Shift: 0, Width: 8, Unit: "4ms"},
			{Key: "acc", Name: "碰撞加速度", Shift: 8, Width: 8, Unit: "0.1g", Max: 79, Help: "0~79，默认 10"},
		}}}})),
	p(0x005E, "侧翻报警参数设置", "WORD", "报警", "30", unit("度"), hint("侧翻角度，默认 30 度")),
	p(0x0032, "违规行驶时段范围", "BYTES", "报警", "00000000", std("2019"), schema(&Schema{Fixed: true, Fields: []SField{
		{Key: "sh", Name: "开始（时）", Size: 1, Max: 23}, {Key: "sm", Name: "开始（分）", Size: 1, Max: 59},
		{Key: "eh", Name: "结束（时）", Size: 1, Max: 23}, {Key: "em", Name: "结束（分）", Size: 1, Max: 59}}}),
		hint("BYTE[4]，精确到分，可跨零点（如 22:50~次日 10:30）。2019 版在该时段内行驶产生违规行驶报警（bit15）；开始=结束表示不启用")),

	p(0x0064, "定时拍照控制", "DWORD", "多媒体", "0", schema(photoSchema("定时时间单位", "定时时间间隔", Opt{0, "秒（小于 5 按 5）"}, Opt{1, "分"}))),
	p(0x0065, "定距拍照控制", "DWORD", "多媒体", "0", schema(photoSchema("定距距离单位", "定距距离间隔", Opt{0, "米（小于 100 按 100）"}, Opt{1, "千米"}))),
	p(0x0070, "图像/视频质量", "DWORD", "多媒体", "5", rng(1, 10), hint("1~10，1 最好")),
	p(0x0071, "亮度", "DWORD", "多媒体", "128", rng(0, 255)),
	p(0x0072, "对比度", "DWORD", "多媒体", "64", rng(0, 127)),
	p(0x0073, "饱和度", "DWORD", "多媒体", "64", rng(0, 127)),
	p(0x0074, "色度", "DWORD", "多媒体", "128", rng(0, 255)),

	p(0x0075, "音视频参数设置", "BYTES", "音视频", "00010019 0f 00000200 00010019 0f 00000200 0003 01", std("JT/T 1078"), schema(avSchema)),
	p(0x0076, "音视频通道列表设置", "BYTES", "音视频", "0200000101000002020000", std("JT/T 1078"), schema(chanSchema)),
	p(0x0077, "单独视频通道参数设置", "BYTES", "音视频", "00", std("JT/T 1078"), schema(chanVideoSchema)),
	p(0x0079, "特殊报警录像参数设置", "BYTES", "音视频", "140a05", std("JT/T 1078"), schema(&Schema{Fields: []SField{
		{Key: "threshold", Name: "特殊报警录像存储阈值", Size: 1, Unit: "%", Min: 1, Max: 99, Help: "占用主存储器百分比"},
		{Key: "duration", Name: "特殊报警录像持续时间", Size: 1, Unit: "分钟"},
		{Key: "start", Name: "特殊报警标识起始时间", Size: 1, Unit: "分钟", Help: "报警发生前进行标记的录像时间"}}})),
	p(0x007A, "视频相关报警屏蔽字", "DWORD", "音视频", "0", std("JT/T 1078"), bitsOf(videoAlarmNames), hint("与 0x14 视频报警标志对应，相应位为 1 则屏蔽")),
	p(0x007B, "图像分析报警参数设置", "BYTES", "音视频", "2d03", std("JT/T 1078"), schema(&Schema{Fields: []SField{
		{Key: "load", Name: "车辆核载人数", Size: 1, Unit: "人", Help: "客运车辆核定载客人数，视频分析结果超过时产生报警"},
		{Key: "fatigue", Name: "疲劳程度阈值", Size: 1, Help: "视频分析疲劳驾驶报警阈值，超过时产生报警"}}})),
	p(0x007C, "终端休眠唤醒模式设置", "BYTES", "音视频", "0001000100000000000000000000000000000000", std("JT/T 1078"), schema(sleepSchema)),

	p(0x0080, "车辆里程表读数", "DWORD", "车辆", "0", unit("1/10 km"), hint("与页面“总里程”联动")),
	p(0x0081, "车辆所在的省域 ID", "WORD", "车辆", "44", hint("GB/T 2260 行政区划代码前两位")),
	p(0x0082, "车辆所在的市域 ID", "WORD", "车辆", "100", hint("GB/T 2260 行政区划代码后四位")),
	p(0x0083, "机动车号牌", "STRING", "车辆", "", hint("与终端配置“车牌”联动")),
	p(0x0084, "车牌颜色", "BYTE", "车辆", "2", optPlate, hint("JT/T 697.7；0 未上牌时注册上报 VIN")),

	p(0x0090, "GNSS 定位模式", "BYTE", "GNSS", "3", bitsOf(gnssBits)),
	p(0x0091, "GNSS 波特率", "BYTE", "GNSS", "1", opts(Opt{0, "4800"}, Opt{1, "9600"}, Opt{2, "19200"}, Opt{3, "38400"}, Opt{4, "57600"}, Opt{5, "115200"})),
	p(0x0092, "GNSS 模块详细定位数据输出频率", "BYTE", "GNSS", "1", opts(Opt{0, "500ms"}, Opt{1, "1000ms"}, Opt{2, "2000ms"}, Opt{3, "3000ms"}, Opt{4, "4000ms"})),
	p(0x0093, "GNSS 模块详细定位数据采集频率", "DWORD", "GNSS", "1", unit("秒")),
	p(0x0094, "GNSS 模块详细定位数据上传方式", "BYTE", "GNSS", "0", opts(Opt{0, "本地存储，不上传"}, Opt{1, "按时间间隔上传"}, Opt{2, "按距离间隔上传"},
		Opt{0x0B, "按累计时间上传，达到传输时间后自动停止"}, Opt{0x0C, "按累计距离上传，达到距离后自动停止"}, Opt{0x0D, "按累计条数上传，达到条数后自动停止"})),
	p(0x0095, "GNSS 模块详细定位数据上传设置", "DWORD", "GNSS", "0", hint("上传方式 0x01/0x0B 单位秒，0x02/0x0C 单位米，0x0D 单位条")),

	p(0x0100, "CAN 总线通道 1 采集时间间隔", "DWORD", "CAN", "0", unit("ms"), hint("0 表示不采集")),
	p(0x0101, "CAN 总线通道 1 上传时间间隔", "WORD", "CAN", "0", unit("秒"), hint("0 表示不上传")),
	p(0x0102, "CAN 总线通道 2 采集时间间隔", "DWORD", "CAN", "0", unit("ms"), hint("0 表示不采集")),
	p(0x0103, "CAN 总线通道 2 上传时间间隔", "WORD", "CAN", "0", unit("秒"), hint("0 表示不上传")),
	p(0x0110, "CAN 总线 ID 单独采集设置", "BYTES", "CAN", "0000000000000000", schema(canSchema),
		hint("BYTE[8]；0x0111~0x01FF 用于其他 CAN 总线 ID，可在页面添加")),

	p(0xF364, "高级驾驶辅助系统参数（ADAS）", "BYTES", "主动安全", adasDefault, std("JSATL12"), schema(adasSchema)),
	p(0xF365, "驾驶员状态监测系统参数（DSM）", "BYTES", "主动安全", dsmDefault, std("JSATL12 + 粤标扩展"), schema(dsmSchema)),
	p(0xF366, "胎压监测系统参数（TPMS）", "BYTES", "主动安全", tpmsDefault, std("JSATL12"), schema(tpmsSchema)),
	p(0xF367, "盲区监测系统参数（BSD）", "BYTES", "主动安全", "0505", std("JSATL12"), schema(&Schema{Fields: []SField{
		{Key: "rear", Name: "后方接近报警时间阈值", Size: 1, Unit: "秒", Min: 1, Max: 10},
		{Key: "side", Name: "侧后方接近报警时间阈值", Size: 1, Unit: "秒", Min: 1, Max: 10}}})),
}

var paramIndex = func() map[int]*ParamDef {
	m := map[int]*ParamDef{}
	for i := range paramDefs {
		d := &paramDefs[i]
		d.Def = strings.ReplaceAll(d.Def, " ", "")
		m[d.ID] = d
	}
	return m
}()

// canExtra 0x0111~0x01FF 共用 0x0110 的定义
var canExtra = map[int]*ParamDef{}

func isCANID(id int) bool { return id >= 0x0110 && id <= 0x01FF }

// pdef 参数定义；未知参数返回 nil
func pdef(id int) *ParamDef {
	if d := paramIndex[id]; d != nil {
		return d
	}
	if isCANID(id) {
		if d := canExtra[id]; d != nil {
			return d
		}
		d := *paramIndex[0x0110]
		d.ID, d.Name, d.Help = id, fmt.Sprintf("CAN 总线 ID 单独采集设置 #%d", id-0x0110+1), "BYTE[8]"
		canExtra[id] = &d
		return &d
	}
	return nil
}

func paramType(id int) string {
	if d := pdef(id); d != nil {
		return d.Type
	}
	if d := gdParamIndex[id]; d != nil { // 粤标独有参数（0xF370）
		return d.Type
	}
	return "BYTES" // 未知参数按原始字节保存并原样回报
}

// Params 参数值（字符串形式；BYTES 为 hex）
type Params map[int]string

func defaultParams() Params {
	p := Params{}
	for _, d := range paramDefs {
		p[d.ID] = d.Def
	}
	return p
}

func (p Params) Int(id int) int {
	n, _ := strconv.ParseInt(p[id], 10, 64)
	return int(n)
}

// encode 参数值 -> 线上字节
func (p Params) encode(id int) []byte { return encodeValue(id, p[id]) }

func encodeValue(id int, v string) []byte {
	switch paramType(id) {
	case "DWORD":
		n, _ := strconv.ParseUint(v, 10, 32)
		return u32(int(n))
	case "WORD":
		n, _ := strconv.ParseUint(v, 10, 16)
		return u16(int(n))
	case "BYTE":
		n, _ := strconv.ParseUint(v, 10, 8)
		return []byte{byte(n)}
	case "STRING":
		return gbk(v)
	}
	b, _ := hex.DecodeString(strings.ReplaceAll(v, " ", ""))
	return b
}

// decodeParam 线上字节 -> 参数值
func decodeParam(id int, b []byte) string {
	switch paramType(id) {
	case "DWORD":
		if len(b) >= 4 {
			return fmt.Sprint(binary.BigEndian.Uint32(b))
		}
	case "WORD":
		if len(b) >= 2 {
			return fmt.Sprint(binary.BigEndian.Uint16(b))
		}
	case "BYTE":
		if len(b) >= 1 {
			return fmt.Sprint(b[0])
		}
	case "STRING":
		return ungbk(b)
	}
	return hex.EncodeToString(b)
}

// validateType 只校验格式（数值位宽 / hex），不校验取值范围
func validateType(id int, v string) (string, error) {
	switch t := paramType(id); t {
	case "DWORD", "WORD", "BYTE":
		bits := map[string]int{"DWORD": 32, "WORD": 16, "BYTE": 8}[t]
		if _, err := strconv.ParseUint(strings.TrimSpace(v), 10, bits); err != nil {
			return "", fmt.Errorf("参数 %04X 需要 %d 位无符号整数", id, bits)
		}
	case "BYTES":
		if _, err := hex.DecodeString(strings.ReplaceAll(v, " ", "")); err != nil {
			return "", fmt.Errorf("参数 %04X 需要 hex", id)
		}
	}
	return v, nil
}

// validateParam 校验 UI/命令行输入的参数值（按类型、取值范围、结构体长度）
func validateParam(id int, v string) (string, error) {
	d := pdef(id)
	name := fmt.Sprintf("参数 %04X", id)
	if d != nil {
		name = fmt.Sprintf("参数 %04X「%s」", id, d.Name)
	}
	switch paramType(id) {
	case "DWORD", "WORD", "BYTE":
		bits := map[string]int{"DWORD": 32, "WORD": 16, "BYTE": 8}[paramType(id)]
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, bits)
		if err != nil {
			return "", fmt.Errorf("%s 需要 %d 位无符号整数", name, bits)
		}
		if d != nil && d.Min != nil && int64(n) < *d.Min || d != nil && d.Max != nil && int64(n) > *d.Max {
			return "", fmt.Errorf("%s 取值范围 %d~%d", name, *d.Min, *d.Max)
		}
		if d != nil && len(d.Opts) > 0 {
			ok := false
			for _, o := range d.Opts {
				ok = ok || o.V == int64(n)
			}
			if !ok {
				return "", fmt.Errorf("%s 取值 %d 不在可选值内", name, n)
			}
		}
		return fmt.Sprint(n), nil
	case "STRING":
		if l := len(gbk(v)); l > 255 || d != nil && d.MaxLen > 0 && l > d.MaxLen {
			return "", fmt.Errorf("%s 过长（%d 字节）", name, l)
		}
		return v, nil
	}
	v = strings.ToLower(strings.ReplaceAll(v, " ", ""))
	b, err := hex.DecodeString(v)
	if err != nil {
		return "", fmt.Errorf("%s 需要 hex", name)
	}
	if len(b) > 255 {
		return "", fmt.Errorf("%s 超过 255 字节", name)
	}
	if d != nil && d.Schema != nil && d.Schema.Fixed && len(b) != d.Schema.size() {
		return "", fmt.Errorf("%s 需要 %d 字节，实际 %d 字节", name, d.Schema.size(), len(b))
	}
	return v, nil
}

// items 编码参数项列表：ID(4) 长度(1) 值；ids 为空表示全部
func (p Params) items(ids []int) (int, []byte) {
	if len(ids) == 0 {
		for id := range p {
			ids = append(ids, id)
		}
		sort.Ints(ids)
	}
	var out []byte
	n := 0
	for _, id := range ids {
		if _, ok := p[id]; !ok {
			continue
		}
		v := p.encode(id)
		if len(v) > 255 {
			v = v[:255]
		}
		out = cat(out, u32(id), []byte{byte(len(v))}, v)
		n++
	}
	return n, out
}
