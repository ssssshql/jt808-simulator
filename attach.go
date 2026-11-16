package main

// 主动安全（JSATL12/粤标）：0x64 ADAS 0x65 DSM 0x66 TPMS 0x67 BSD 报警；9208 后连附件服务器 1210/1211/码流/1212 <- 9212

import (
	_ "embed"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

//go:embed assets/alarm.mp4
var alarmMP4 []byte

var adasKinds = map[string]int{"adas": 0x64, "dsm": 0x65, "tpms": 0x66, "bsd": 0x67}

var adasTypeNames = map[int]map[int]string{
	0x64: {1: "前向碰撞", 2: "车道偏离", 3: "车距过近", 4: "行人碰撞", 5: "频繁变道", 6: "道路标识超限", 7: "障碍物", 8: "驾驶辅助功能失效",
		0x10: "道路标志识别事件", 0x11: "主动抓拍事件"},
	0x65: {1: "疲劳驾驶", 2: "接打电话", 3: "抽烟", 4: "分神驾驶", 5: "驾驶员异常", 6: "探头遮挡", 7: "驾驶员变更", 8: "双手同时脱离方向盘",
		0x10: "自动抓拍事件", 0x11: "驾驶员变更事件"},
	0x66: {1: "胎压过高", 2: "胎压过低", 3: "胎温过高", 4: "传感器异常", 5: "胎压不平衡", 6: "慢漏气", 7: "电池电量低"},
	0x67: {1: "后方接近", 2: "左侧后方接近", 3: "右侧后方接近"},
}

type adasAlarm struct {
	attr, typ, level int
}

// adasAttr 构造报警附加信息与报警标识。调用方持有 t.mu
func (t *Terminal) adasAttr(attr, typ, level, files, flagSt int) ([]byte, []byte) {
	if t.Cfg.yue() {
		return t.gdAttr(attr, typ, level, files, flagSt)
	}
	if flagSt < 0 {
		flagSt = 1
	}
	ver := t.Cfg.ver()
	didLen, rsv := 7, 1
	if ver >= V2019 {
		didLen, rsv = 30, 2
	}
	t.alarmSeq++
	now := bcdNow()
	flag := cat(fixed(t.Cfg.DeviceID, didLen), now, []byte{byte(t.alarmSeq % 256), byte(files)}, make([]byte, rsv))
	tail := cat([]byte{byte(t.speed)}, u16(t.alt), u32(int(t.Lat*1e6)), u32(int(t.Lng*1e6)), now, u16(int(t.statusWord()&0xFFFF)), flag)
	id := u32(int(time.Now().Unix()) + t.alarmSeq)
	var v []byte
	switch attr {
	case 0x64: // 标志 类型 级别 前车车速 前车距离 偏离类型 道路标志类型 道路标志数据
		dev := 0
		if typ == 2 {
			dev = 1
		}
		v = cat(id, []byte{byte(flagSt), byte(typ), byte(level), 40, 15, byte(dev), 0, 0}, tail)
	case 0x65: // 标志 类型 级别 疲劳程度 预留(4)
		v = cat(id, []byte{byte(flagSt), byte(typ), byte(level), 5}, make([]byte, 4), tail)
	case 0x66: // 标志 车速.. 事件列表：位置 类型(2) 胎压 温度 电量
		v = cat(id, []byte{byte(flagSt)}, tail, []byte{1, 0}, u16(1<<(typ-1)), u16(320), u16(65), u16(80))
	case 0x67: // 标志 类型
		v = cat(id, []byte{byte(flagSt), byte(typ)}, tail)
	}
	t.adasPending[string(flag)] = adasAlarm{attr, typ, level}
	return cat([]byte{byte(attr), byte(len(v))}, v), flag
}

// raiseADAS 立即上报一条带主动安全报警附加信息的 0200
// flag：标志状态 0 不可用 1 开始 2 结束，-1 自动
func (t *Terminal) raiseADAS(kind string, typ, level, files, flag int) error {
	attr, ok := adasKinds[kind]
	if !ok {
		return fmt.Errorf("kind 只能是 adas/dsm/tpms/bsd")
	}
	if flag < -1 || flag > 2 {
		return fmt.Errorf("标志状态只能是 0/1/2")
	}
	if files < 0 || files > 255 || level < 1 || level > 2 && attr != 0x66 && attr != 0x67 {
		return fmt.Errorf("报警级别/附件数量无效")
	}
	t.mu.Lock()
	names := t.Cfg.typeNames()
	std := t.Cfg.stdName()
	if names[attr][typ] == "" {
		t.mu.Unlock()
		return fmt.Errorf("%s 不支持类型 0x%02X（%s）", kind, typ, std)
	}
	extra, _ := t.adasAttr(attr, typ, level, files, flag)
	body := t.locBody(extra)
	s := fmt.Sprintf("%s %s 级别%d 附件%d", strings.ToUpper(kind), names[attr][typ], level, files)
	if t.Cfg.yue() {
		s = "[粤标] " + s
	}
	t.mu.Unlock()
	_, _, err := t.send(0x0200, body, s)
	return err
}

// attachFiles 按 JSATL12 命名：文件类型_通道_报警类型_序号_报警编号.后缀
// 图片优先取媒体源的当前画面（视频/摄像头抓帧或上传的图片），视频优先用上传的源视频
func (t *Terminal) attachFiles(a adasAlarm, total int, no string) []AttFile {
	ch := map[int]int{0x64: 64, 0x65: 65, 0x66: 66, 0x67: 67}[a.attr]
	code := fmt.Sprintf("%02X%02X", a.attr, a.typ)
	var files []AttFile
	for i := 0; i < total; i++ {
		if i == total-1 && total > 1 {
			files = append(files, AttFile{fmt.Sprintf("02_%d_%s_%d_%s.mp4", ch, code, i, no), 2, t.attachmentVideo()})
		} else {
			img := t.currentImage()
			if img == nil {
				img = makeJPEG(ch, 2, 3, time.Now())
			}
			files = append(files, AttFile{fmt.Sprintf("00_%d_%s_%d_%s.jpg", ch, code, i, no), 0, img})
		}
	}
	return files
}

func init() { handlers[0x9208] = (*Terminal).h9208 }

func (t *Terminal) h9208(m *Msg) {
	info, err := parse9208(m.Version, m.Body)
	if err != nil {
		t.errf("%v", err)
		t.ack(m, 2)
		return
	}
	t.ack(m, 0)
	t.mu.Lock()
	a, ok := t.adasPending[string(info.Flag)]
	auto, host, yue := t.Cfg.AutoAttach, t.Cfg.AttachHost, t.Cfg.yue()
	var rec []byte
	if yue {
		rec = t.gdRecordFile(20)
	}
	t.mu.Unlock()
	if !ok {
		a = adasAlarm{0x64, 1, 1}
	}
	no := strings.TrimRight(string(info.AlarmNo), "\x00")
	t.addInbox("attach", "报警附件上传指令", fmt.Sprintf("%s:%d 报警编号 %s 附件 %d", info.IP, info.TCP, no, info.FileTotal), 0, nil)
	if !auto {
		t.logf("自动上传附件已关闭，忽略 9208")
		return
	}
	if host == "" {
		host = info.IP
	}
	files := t.attachFiles(a, max(info.FileTotal, 1), no)
	if yue {
		files = t.gdAttachFiles(a, max(info.FileTotal, 1), no, rec)
	}
	if err := t.uploadAttachments(host, info, files); err != nil {
		t.errf("附件上传失败: %v", err)
	}
}

type attConn struct {
	c      net.Conn
	r      FrameReader
	queue  []*Msg
	phone  string
	ver    int
	serial int
	t      *Terminal
}

func (a *attConn) send(id int, body []byte, summary string) error {
	a.serial++
	f := Frame(id, a.phone, a.serial, body, a.ver, 0, 0)
	a.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := a.c.Write(f); err != nil {
		return err
	}
	a.t.logMsg("up", id, a.serial, len(body), f, "[附件] "+summary)
	return nil
}

// wait 等待指定消息（超时返回 nil）
func (a *attConn) wait(id int, d time.Duration) *Msg {
	deadline := time.Now().Add(d)
	buf := make([]byte, 4096)
	for {
		for i, m := range a.queue {
			if m.ID == id {
				a.queue = append(a.queue[:i], a.queue[i+1:]...)
				return m
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		a.c.SetReadDeadline(deadline)
		n, err := a.c.Read(buf)
		if err != nil {
			return nil
		}
		for _, m := range a.r.Feed(buf[:n]) {
			a.t.logMsg("down", m.ID, m.Serial, len(m.Body), m.Raw, "[附件]")
			a.queue = append(a.queue, m)
		}
	}
}

// writeData 文件数据码流（不转义），按 64KB 分段
func (a *attConn) writeData(name string, data []byte, off, n int) error {
	for p := off; p < off+n; p += 65536 {
		seg := data[p:min(p+65536, off+n)]
		a.c.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := a.c.Write(dataPacket(name, p, seg)); err != nil {
			return err
		}
	}
	return nil
}

// uploadAttachments 连接附件服务器上传。粤标：上传中链接异常断开时，重连并以 1210 信息类型 0x01（补传）
// 重新发送报警附件信息，列出断开前未上传和未完成的文件（T/GDRTA 002—2020 5.6.2），最多重连 3 次
func (t *Terminal) uploadAttachments(host string, info *Info9208, files []AttFile) error {
	t.mu.Lock()
	yue := t.Cfg.yue()
	t.mu.Unlock()
	infoType := 0
	for try := 0; ; try++ {
		done, err := t.uploadOnce(host, info, files, infoType)
		if err == nil || !yue || done < 0 || try >= 3 {
			return err
		}
		files = files[done:]
		infoType = 1
		t.errf("[附件] %v；1 秒后重连附件服务器补传剩余 %d 个文件", err, len(files))
		time.Sleep(time.Second)
	}
}

// uploadOnce 一次附件服务器会话，返回已完成的文件数（连接失败返回 -1）
func (t *Terminal) uploadOnce(host string, info *Info9208, files []AttFile, infoType int) (int, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(info.TCP))
	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return -1, fmt.Errorf("连接附件服务器 %s 失败: %v（可在设备配置里覆盖附件服务器 IP）", addr, err)
	}
	defer c.Close()
	t.mu.Lock()
	a := &attConn{c: c, phone: t.Cfg.Phone, ver: t.Cfg.ver(), t: t}
	did := t.Cfg.DeviceID
	t.mu.Unlock()
	t.logf("[附件] 已连接附件服务器 %s，%d 个文件%s", addr, len(files), map[int]string{1: "（补传）"}[infoType])
	if err := a.send(0x1210, b1210(a.ver, did, info.Flag, info.AlarmNo, files, infoType), fmt.Sprintf("%d 个附件%s", len(files), map[int]string{1: " 补传"}[infoType])); err != nil {
		return 0, err
	}
	a.wait(0x8001, 5*time.Second)
	for i, f := range files {
		if err := a.send(0x1211, b1211(f.Name, f.Type, len(f.Data)), f.Name); err != nil {
			return i, err
		}
		a.wait(0x8001, 5*time.Second)
		if err := a.writeData(f.Name, f.Data, 0, len(f.Data)); err != nil {
			return i, err
		}
		for try := 0; ; try++ {
			if err := a.send(0x1212, b1211(f.Name, f.Type, len(f.Data)), f.Name); err != nil {
				return i, err
			}
			m := a.wait(0x9212, 10*time.Second)
			if m == nil {
				return i, fmt.Errorf("%s 等待 9212 超时", f.Name)
			}
			// 9212：文件名(长度+串) 类型 结果(0 完成 1 需补传) 补传包数 [偏移 长度]...
			r := newReader(m.Body)
			r.lstr()
			r.u8()
			res, n := r.u8(), r.u8()
			if res == 0 || r.err != nil {
				t.logf("[附件] %s %d 字节 上传完成", f.Name, len(f.Data))
				break
			}
			if try >= 3 {
				return i, fmt.Errorf("%s 补传 3 次仍未完成", f.Name)
			}
			for j := 0; j < n && r.err == nil; j++ {
				off, ln := r.u32(), r.u32()
				if off+ln <= len(f.Data) {
					a.writeData(f.Name, f.Data, off, ln)
				}
			}
			t.logf("[附件] %s 按 9212 补传 %d 段", f.Name, n)
		}
	}
	return len(files), nil
}
