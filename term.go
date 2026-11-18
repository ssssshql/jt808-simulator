package main

// 终端模型：配置 + 运行状态 + 本地持久化（state/<phone>.json，模拟终端掉电保存）

import (
	"crypto/rsa"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Host          string            `json:"host"`
	Port          int               `json:"port"`
	Phone         string            `json:"phone"`
	Version       string            `json:"version"` // 2013 / 2019
	Token         string            `json:"token"`
	Plate         string            `json:"plate"`
	PlateColor    int               `json:"plateColor"`
	VIN           string            `json:"vin"`
	DeviceID      string            `json:"deviceId"`
	Model         string            `json:"model"`
	Maker         string            `json:"maker"`
	ICCID         string            `json:"iccid"`
	IMEI          string            `json:"imei"`
	HwVer         string            `json:"hwVer"`
	FwVer         string            `json:"fwVer"`
	AutoReconnect bool              `json:"autoReconnect"`
	AttachHost    string            `json:"attachHost"` // 覆盖 9208 下发的附件服务器 IP（为空用下发值）
	AutoAttach    bool              `json:"autoAttach"`
	UpRSA         bool              `json:"upRsa"`               // 上行RSA加密（需已收到平台 8A00 公钥）
	Transport     string            `json:"transport,omitempty"` // tcp（默认）/ udp
	Reply8500     string            `json:"reply8500,omitempty"` // 8500 应答方式：空 = 只回 0500；"ack0500" = 先回 0001 通用应答再回 0500
	SafetyStd     string            `json:"safetyStd,omitempty"` // 主动安全标准：空 = 苏标 JSATL12（默认）；"gd" = 粤标 T/GDRTA 002-2020
	MediaSrc      string            `json:"mediaSrc,omitempty"`  // 附件画面来源（全部通道默认）：空 = 合成测试图；image / video / camera（见 mediasrc.go）
	MediaCh       map[string]string `json:"mediaCh,omitempty"`   // 按通道覆盖：键为通道号 64/65/66/67，值同上；未列的通道沿用 MediaSrc
	NoStamp       bool              `json:"noStamp,omitempty"`   // 附件画面不烧时间水印（默认烧，见 stamp.go）
}

// transport 传输方式：udp 或 tcp（空/其它值按 tcp）
func (c *Config) transport() string {
	if c.Transport == "udp" {
		return "udp"
	}
	return "tcp"
}

// ver 帧格式版本。粤标基于 JT/T 808-2019，选择粤标时强制按 2019 收发
func (c *Config) ver() int {
	if c.Version == "2013" && !c.yue() {
		return V2013
	}
	return V2019
}

type Area struct {
	Kind     int          `json:"kind"` // 1 圆 2 矩形 3 多边形 4 路线
	ID       int          `json:"id"`
	Attr     int          `json:"attr"`
	Name     string       `json:"name"`
	Points   [][2]float64 `json:"points"` // [lat, lng]；圆=中心，矩形=左上/右下
	Radius   int          `json:"radius"`
	Start    time.Time    `json:"start"`
	End      time.Time    `json:"end"`
	MaxSpeed int          `json:"maxSpeed"`
	Duration int          `json:"duration"`
	Night    int          `json:"night"`
	Raw      string       `json:"raw"`            // 原始区域项 hex，0608 查询时原样回报
	Segs     []RouteSeg   `json:"segs,omitempty"` // 路线：拐点/路段
}

type InboxItem struct {
	Seq      int64    `json:"seq"`
	T        int64    `json:"t"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	Serial   int      `json:"serial,omitempty"`
	Answers  []Answer `json:"answers,omitempty"`
	Answered bool     `json:"answered,omitempty"`
}

type Answer struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type MediaItem struct {
	ID      int    `json:"id"`
	Type    int    `json:"type"` // 0 图像 1 音频 2 视频
	Format  int    `json:"format"`
	Channel int    `json:"channel"`
	Event   int    `json:"event"`
	T       int64  `json:"t"`
	Size    int    `json:"size"`
	Loc     string `json:"loc"` // 拍摄时 28 字节位置 hex
	Res     int    `json:"res"`
	Quality int    `json:"quality"`
	Dur     int    `json:"dur"`
}

type Driver struct {
	Name      string `json:"name"`
	LicenseNo string `json:"licenseNo"`
	Org       string `json:"org"`
	ValidTo   string `json:"validTo"` // YYYYMMDD
	IDCard    string `json:"idCard"`
	OnDuty    bool   `json:"onDuty"`
}

// Saved 持久化部分
type Saved struct {
	Cfg       Config           `json:"cfg"`
	Params    Params           `json:"params"`
	Lng       float64          `json:"lng"`
	Lat       float64          `json:"lat"`
	Mileage   float64          `json:"mileage"` // km
	Fuel      float64          `json:"fuel"`    // L
	StatusOn  uint32           `json:"statusOn"`
	Areas     []Area           `json:"areas"`
	Phonebook []map[string]any `json:"phonebook"`
	Events    map[int]string   `json:"events"`
	Menus     map[int]string   `json:"menus"`
	Inbox     []InboxItem      `json:"inbox"`
	Media     []MediaItem      `json:"media"`
	MediaSeq  int              `json:"mediaSeq"`
	Driver    Driver           `json:"driver"`
	Blind     []string         `json:"blind"` // 盲区位置体 hex
	DayDrive  int              `json:"dayDrive"`
	DayKey    string           `json:"dayKey"`
	Rec       RecorderState    `json:"rec"`
	ExtSignal uint32           `json:"extSignal"`           // 0x25 扩展车辆信号状态位
	IOStatus  uint16           `json:"ioStatus"`            // 0x2A IO状态位
	Analog    [2]int           `json:"analog"`              // 0x2B 模拟量 AD0 / AD1
	RSAKey    string           `json:"rsaKey,omitempty"`    // 终端 RSA 私钥（PKCS#1 DER hex）
	PlatRSA   PlatKey          `json:"platRsa"`             // 平台 RSA 公钥（8A00）
	Replay    *Replay          `json:"replay,omitempty"`    // 导入的回放轨迹（GPX/CSV）
	GD        GDState          `json:"gd"`                  // 粤标外设状态 / 异常信息 / 升级模拟
	Tires     []int            `json:"tires,omitempty"`     // 0x05 胎压（2019），最多 30 个轮子，0~254
	CabinTemp *int             `json:"cabinTemp,omitempty"` // 0x06 车厢温度（2019），℃
}

type Terminal struct {
	mu sync.Mutex
	Saved
	dirty bool

	// 多终端：编号、日志、生命周期
	ID    string
	hub   *logHub
	quit  chan struct{} // 关闭后 motionLoop / saveLoop 退出（删除终端时）
	phone atomic.Value  // Cfg.Phone 副本（string），供管理器无锁读取
	mgr   *Manager      // 多终端管理器（命令行单终端模式为 nil）

	// 连接
	conn       net.Conn
	wmu        sync.Mutex
	status     string // offline / connecting / online / reconnecting
	online     time.Time
	reconnects int
	lastErr    string
	want       bool // 用户期望在线
	sessStop   chan struct{}
	sessDone   chan struct{}
	serial     int
	pending    map[int]*pend
	waiters    map[string]chan *Msg
	upCache    map[int][][]byte // 分包上行缓存：首包流水号 -> 各包帧
	upOrder    []int
	downParts  map[int]*partial
	lastUp     time.Time
	hostOver   string // 8105 控制连接时的临时服务器
	portOver   int

	// 运动与报警
	motion    Motion
	speed     float64
	dir       float64
	alt       int
	sats      int
	signal    int
	track     [][2]float64
	alarmMan  uint32 // 手动置位
	alarmAuto uint32 // 自动判定
	alarmClr  uint32 // 8203/8001 确认后清除（直到条件消失）
	videoMan  uint32
	nextRpt   time.Time
	lastRpt   struct {
		lng, lat, dir float64
		status, alarm uint32
	}
	tempUntil   time.Time
	tempInt     int
	contDrive   int // 连续驾驶秒
	restSec     int
	parkSec     int
	overSec     int
	inArea      map[int]bool
	areaOverSec int
	extra       []byte // 下一条 0200 附带的一次性附加信息
	forceRpt    bool
	powerOff    bool
	lastPhoto   map[int]time.Time
	lastPhotoKm float64
	alarmSeq    int
	gdSeq       int    // 粤标报警标识号序号：同一时间点从 0 累加
	gdSeqKey    string // 上一次报警的秒级时间
	pendEvt     int    // 0x04 人工确认报警事件 ID

	rp     replayRun         // 轨迹回放游标
	segIn  map[int]*segVisit // 路线 ID -> 当前所在路段
	rsaKey *rsa.PrivateKey

	// 下行应答策略
	replyMode  string
	replyDelay float64

	// 其它
	counters    struct{ Up, Down, Retrans int }
	inboxSeq    int64
	streams     map[int]*avStream
	ftpCtl      map[int]chan int
	adasPending map[string]adasAlarm

	// 媒体源：前端回传的画面历史（每通道最新帧在前；附件多张图片按序号取不同时刻），键为通道号（"0" = 全部通道）
	frames map[string][]frameBuf
	clips  map[string]clipBuf // 视频/摄像头最近一段录制（附件视频用），键同上
}

var stateDir = "state"

func NewTerminal(cfg Config) *Terminal {
	t := &Terminal{hub: newHub(), quit: make(chan struct{}), status: "offline", replyMode: "auto", pending: map[int]*pend{}, waiters: map[string]chan *Msg{},
		upCache: map[int][][]byte{}, downParts: map[int]*partial{}, inArea: map[int]bool{}, streams: map[int]*avStream{},
		ftpCtl: map[int]chan int{}, lastPhoto: map[int]time.Time{}, segIn: map[int]*segVisit{}, adasPending: map[string]adasAlarm{}, sats: 12, signal: 25, alt: 20}
	t.Params = defaultParams()
	t.Lng, t.Lat, t.Mileage, t.Fuel = 113.324520, 23.106470, 12345.6, 80
	t.frames, t.clips = map[string][]frameBuf{}, map[string]clipBuf{}
	t.StatusOn = 1 | 1<<18 | 1<<19 // ACC 开 + GPS/北斗定位
	t.Events, t.Menus = map[int]string{}, map[int]string{}
	t.MediaSeq = int(time.Now().Unix() % 100000)
	t.Driver = Driver{Name: "张三", LicenseNo: "440100199001011234", Org: "广州市交通运输局", ValidTo: "20301231", IDCard: "440100199001011234"}
	t.motion = Motion{Mode: "stop", Speed: 60}
	t.Cfg = Config{Phone: cfg.Phone, AutoReconnect: true, AutoAttach: true, PlateColor: 2} // 车牌颜色默认黄色；0 = 未上牌（注册上报 VIN）
	t.load()
	t.Cfg.overlay(cfg) // 命令行显式指定的值优先于保存值
	t.fillDefaults()
	t.phone.Store(t.Cfg.Phone)
	return t
}
