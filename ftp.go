package main

// 9206 文件上传：被动模式 FTP 上传录像（内置 H.264 片段重复填充到约 1MB），完成后 1206；9207 暂停/继续/取消

import (
	"fmt"
	"net"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ftpTask struct {
	host, user, pass, path, name string
	port                         int
	data                         []byte
}

var pasvRe = regexp.MustCompile(`(\d+),(\d+),(\d+),(\d+),(\d+),(\d+)`)

// ftpUpload ctl: 0 暂停 1 继续 2 取消
func ftpUpload(task ftpTask, ctl chan int) error {
	c, err := textproto.Dial("tcp", net.JoinHostPort(task.host, strconv.Itoa(task.port)))
	if err != nil {
		return err
	}
	defer c.Close()
	cmd := func(expect int, format string, a ...any) (string, error) {
		if format != "" {
			if err := c.PrintfLine(format, a...); err != nil {
				return "", err
			}
		}
		code, msg, err := c.ReadResponse(expect)
		if err != nil {
			return msg, fmt.Errorf("FTP %d %s", code, msg)
		}
		return msg, nil
	}
	if _, err := cmd(220, ""); err != nil {
		return err
	}
	if _, err := cmd(0, "USER %s", task.user); err != nil {
		return err
	}
	if task.pass != "" {
		if _, err := cmd(230, "PASS %s", task.pass); err != nil {
			return err
		}
	}
	cmd(200, "TYPE I")
	for _, d := range strings.Split(strings.Trim(task.path, "/"), "/") {
		if d != "" {
			cmd(0, "MKD %s", d)
			if _, err := cmd(250, "CWD %s", d); err != nil {
				return err
			}
		}
	}
	msg, err := cmd(227, "PASV")
	if err != nil {
		return err
	}
	p := pasvRe.FindStringSubmatch(msg)
	if p == nil {
		return fmt.Errorf("无法解析 PASV 应答: %s", msg)
	}
	p1, _ := strconv.Atoi(p[5])
	p2, _ := strconv.Atoi(p[6])
	dc, err := net.DialTimeout("tcp", net.JoinHostPort(task.host, strconv.Itoa(p1*256+p2)), 10*time.Second)
	if err != nil {
		return err
	}
	if err := c.PrintfLine("STOR %s", task.name); err != nil {
		dc.Close()
		return err
	}
	if _, _, err := c.ReadResponse(1); err != nil {
		dc.Close()
		return err
	}
	for off := 0; off < len(task.data); {
		select {
		case v := <-ctl:
			for v == 0 { // 暂停：等待继续或取消
				v = <-ctl
			}
			if v == 2 {
				dc.Close()
				return fmt.Errorf("平台取消上传")
			}
		default:
		}
		n := min(32*1024, len(task.data)-off)
		if _, err := dc.Write(task.data[off : off+n]); err != nil {
			dc.Close()
			return err
		}
		off += n
	}
	dc.Close()
	if _, err := cmd(226, ""); err != nil {
		return err
	}
	cmd(221, "QUIT")
	return nil
}

func (t *Terminal) h9206(m *Msg) {
	r := newReader(m.Body)
	task := ftpTask{host: r.lstr(), port: r.u16(), user: r.lstr(), pass: r.lstr(), path: r.lstr()}
	ch, from, to := r.u8(), r.time6(), r.time6()
	if r.err != nil {
		t.ack(m, 2)
		return
	}
	if from.IsZero() {
		from = time.Now().Add(-time.Minute)
	}
	if to.IsZero() {
		to = from.Add(time.Minute)
	}
	task.name = fmt.Sprintf("%s_%d_%s_%s.h264", t.Cfg.Phone, ch, from.Format("060102150405"), to.Format("060102150405"))
	for len(task.data) < 1<<20 {
		task.data = append(task.data, clipH264...)
	}
	ctl := make(chan int, 4)
	t.mu.Lock()
	t.ftpCtl[m.Serial] = ctl
	t.mu.Unlock()
	t.ack(m, 0)
	t.addInbox("av", "文件上传", fmt.Sprintf("通道 %d %s -> ftp://%s:%d/%s", ch, task.name, task.host, task.port, task.path), 0, nil)
	err := ftpUpload(task, ctl)
	t.mu.Lock()
	delete(t.ftpCtl, m.Serial)
	t.mu.Unlock()
	result := 0
	if err != nil {
		t.errf("FTP 上传失败: %v", err)
		result = 1
	} else {
		t.logf("FTP 上传完成 %s %d 字节", task.name, len(task.data))
	}
	t.send(0x1206, cat(u16(m.Serial), []byte{byte(result)}), task.name+" "+map[int]string{0: "成功", 1: "失败"}[result])
}

func (t *Terminal) h9207(m *Msg) {
	r := newReader(m.Body)
	serial, cmd := r.u16(), r.u8()
	t.mu.Lock()
	ctl := t.ftpCtl[serial]
	t.mu.Unlock()
	if ctl == nil {
		t.ack(m, 1)
		return
	}
	ctl <- cmd
	t.logf("文件上传 %d：%s", serial, map[int]string{0: "暂停", 1: "继续", 2: "取消"}[cmd])
	t.ack(m, 0)
}
