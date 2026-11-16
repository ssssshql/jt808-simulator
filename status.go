package main

// 状态位 / 报警位 / 视频报警位 与 0200 位置信息体

import (
	"bytes"
	"math"
	"time"
)

type Bit struct {
	Bit  int    `json:"bit"`
	Name string `json:"name"`
	On   bool   `json:"on"`
}

var statusNames = map[int]string{0: "ACC 开", 1: "已定位", 2: "南纬", 3: "西经", 4: "停运", 5: "经纬度已加密",
	6: "紧急刹车系统前撞预警", 7: "车道偏移预警",
	8: "载货状态 bit8", 9: "载货状态 bit9", 10: "油路断开", 11: "电路断开", 12: "车门加锁", 13: "门1(前门)开",
	14: "门2(中门)开", 15: "门3(后门)开", 16: "门4(驾驶席门)开", 17: "门5 开", 18: "GPS 定位", 19: "北斗定位",
	20: "GLONASS 定位", 21: "Galileo 定位", 22: "车辆行驶中"}

// 由模拟器计算、不允许手动设置的状态位
const statusComputed = 1<<1 | 1<<2 | 1<<3 | 1<<22

var alarmNames = map[int]string{0: "紧急报警", 1: "超速报警", 2: "疲劳驾驶报警", 3: "危险驾驶行为报警", 4: "GNSS 模块故障",
	5: "GNSS 天线未接或剪断", 6: "GNSS 天线短路", 7: "主电源欠压", 8: "主电源掉电", 9: "显示器故障", 10: "TTS 模块故障",
	11: "摄像头故障", 12: "IC 卡模块故障", 13: "超速预警", 14: "疲劳驾驶预警", 15: "违规行驶报警", 16: "胎压预警",
	17: "右转盲区异常", 18: "当天累计驾驶超时", 19: "超时停车", 20: "进出区域", 21: "进出路线", 22: "路段行驶时间不足/过长",
	23: "路线偏离", 24: "VSS 故障", 25: "油量异常", 26: "车辆被盗", 27: "非法点火", 28: "非法位移", 29: "碰撞侧翻报警",
	30: "侧翻预警", 31: "非法开门"}

var videoAlarmNames = map[int]string{0: "视频信号丢失", 1: "视频信号遮挡", 2: "存储单元故障", 3: "其他视频设备故障",
	4: "客车超员", 5: "异常驾驶行为", 6: "特殊报警录像达到存储阈值"}

// 0x25 扩展车辆信号状态位（JT/T 808 表 31）
var extSignalNames = map[int]string{0: "近光灯", 1: "远光灯", 2: "右转向灯", 3: "左转向灯", 4: "制动", 5: "倒挡",
	6: "雾灯", 7: "示廓灯", 8: "喇叭", 9: "空调", 10: "空挡", 11: "缓速器工作", 12: "ABS 工作", 13: "加热器工作", 14: "离合器"}

// 0x2A IO 状态位（表 32）
var ioNames = map[int]string{0: "深度休眠", 1: "休眠"}

// 收到平台确认后清零的报警位（其余报警维持至条件解除）
const confirmMask uint32 = 1<<0 | 1<<3 | 1<<20 | 1<<21 | 1<<22 | 1<<27 | 1<<28

func bits(names map[int]string, v uint32) []Bit {
	var out []Bit
	for i := 0; i < 32; i++ {
		if n, ok := names[i]; ok {
			out = append(out, Bit{i, n, v&(1<<i) != 0})
		}
	}
	return out
}

// statusWord 调用方持有 t.mu
func (t *Terminal) statusWord() uint32 {
	s := t.StatusOn &^ statusComputed
	if t.alarmWord()&(1<<4|1<<5) == 0 && t.sats >= 3 {
		s |= 1 << 1
	}
	if t.Lat < 0 {
		s |= 1 << 2
	}
	if t.Lng < 0 {
		s |= 1 << 3
	}
	if t.speed > 0.5 && t.Cfg.ver() >= V2019 {
		s |= 1 << 22
	}
	return s
}

func (t *Terminal) alarmWord() uint32 {
	return (t.alarmMan | t.alarmAuto) &^ t.alarmClr &^ uint32(t.Params.Int(0x0050))
}

func (t *Terminal) videoWord() uint32 {
	return t.videoMan &^ uint32(t.Params.Int(0x007A))
}

// confirmAlarms 调用方持有 t.mu
func (t *Terminal) confirmAlarms(mask uint32) {
	mask &= confirmMask
	t.alarmMan &^= mask
	t.alarmClr |= t.alarmAuto & mask
	if mask&(1<<0) != 0 {
		t.pendEvt = 0
	}
}

// setAuto 自动判定的报警；条件解除时同时解除确认标记
func (t *Terminal) setAuto(bit int, on bool) {
	if on {
		t.alarmAuto |= 1 << bit
	} else {
		t.alarmAuto &^= 1 << bit
		t.alarmClr &^= 1 << bit
	}
}

// locBody 0200 位置信息体（基本信息 + 附加信息）。调用方持有 t.mu
func (t *Terminal) locBody(extra []byte) []byte {
	alarm, status := t.alarmWord(), t.statusWord()
	lat, lng := int(math.Abs(t.Lat)*1e6), int(math.Abs(t.Lng)*1e6)
	speed := 0
	if status&(1<<1) != 0 {
		speed = int(t.speed * 10)
	}
	b := cat(u32(int(alarm)), u32(int(status)), u32(lat), u32(lng), u16(t.alt), u16(speed), u16(int(t.dir)%360), bcdNow())
	attr := func(id int, v []byte) { b = cat(b, []byte{byte(id), byte(len(v))}, v) }
	attr(0x01, u32(int(t.Mileage*10)))
	attr(0x02, u16(int(t.Fuel*10)))
	attr(0x03, u16(int(t.speed*10)))
	if t.pendEvt > 0 {
		attr(0x04, u16(t.pendEvt))
	}
	if t.Cfg.ver() >= V2019 { // 2019 新增：0x05 胎压（BYTE[30]，按车头起从左到右，未用 0xFF）、0x06 车厢温度（有符号 WORD，℃）
		if len(t.Tires) > 0 {
			tp := bytes.Repeat([]byte{0xFF}, 30)
			for i, v := range t.Tires {
				if i < 30 {
					tp[i] = byte(v)
				}
			}
			attr(0x05, tp)
		}
		if t.CabinTemp != nil {
			attr(0x06, u16(int(uint16(int16(*t.CabinTemp)))))
		}
	}
	if alarm&(1<<1) != 0 {
		attr(0x11, []byte{0}) // 超速：无特定位置
	}
	if v := t.videoWord(); v != 0 {
		attr(0x14, u32(int(v)))
		if v&1 != 0 {
			attr(0x15, u32(1))
		}
		if v&2 != 0 {
			attr(0x16, u32(1))
		}
		if v&4 != 0 {
			attr(0x17, u16(1))
		}
		if v&(1<<5) != 0 {
			attr(0x18, u16(1)) // 疲劳
		}
	}
	attr(0x25, u32(int(t.ExtSignal)))
	attr(0x2A, u16(int(t.IOStatus)))
	attr(0x2B, u32(t.Analog[1]&0xFFFF<<16|t.Analog[0]&0xFFFF)) // bit0-15 AD0，bit16-31 AD1
	attr(0x30, []byte{byte(t.signal)})
	attr(0x31, []byte{byte(t.sats)})
	return cat(b, t.gdExtraAttrs(), extra)
}

func (t *Terminal) locSummary() string {
	return time.Now().Format("15:04:05") + " " + ftoa(t.Lng, 6) + "," + ftoa(t.Lat, 6) + " " + ftoa(t.speed, 1) + "km/h"
}
