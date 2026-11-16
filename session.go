package main

// 会话：连接 -> 注册(无鉴权码) -> 鉴权 -> 在线(心跳/重传/盲区补报)；断线按退避自动重连，多次失败切备份服务器

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Start 期望上线（自动重连直到 Stop）
func (t *Terminal) Start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.want {
		return
	}
	t.want = true
	t.sessStop = make(chan struct{})
	go t.sessionLoop(t.sessStop)
}

// Stop 主动下线（不发注销）
func (t *Terminal) Stop(reason string) {
	t.mu.Lock()
	if !t.want {
		t.mu.Unlock()
		return
	}
	t.want = false
	close(t.sessStop)
	c := t.conn
	t.mu.Unlock()
	if c != nil {
		c.Close()
	}
	t.logf("终端下线：%s", reason)
}

func (t *Terminal) setStatus(s, errMsg string) {
	t.mu.Lock()
	t.status = s
	if errMsg != "" {
		t.lastErr = errMsg
	}
	if s == "online" {
		t.online = time.Now()
		t.lastErr = ""
	}
	t.mu.Unlock()
}

func (t *Terminal) sessionLoop(stop chan struct{}) {
	fails := 0
	for {
		err := t.runOnce(stop, fails)
		select {
		case <-stop:
			t.setStatus("offline", "")
			return
		default:
		}
		t.mu.Lock()
		auto := t.Cfg.AutoReconnect
		t.mu.Unlock()
		if err != nil {
			fails++
			t.errf("%v", err)
		} else {
			fails = 0
		}
		if !auto {
			t.mu.Lock()
			t.want = false
			t.mu.Unlock()
			t.setStatus("offline", "")
			return
		}
		wait := time.Duration(min(60, 2<<min(fails, 5))) * time.Second
		t.setStatus("reconnecting", "")
		t.mu.Lock()
		t.reconnects++
		t.mu.Unlock()
		t.logf("%v 后重连（第 %d 次失败）", wait, fails)
		select {
		case <-stop:
			t.setStatus("offline", "")
			return
		case <-time.After(wait):
		}
	}
}

// target 本次连接地址：8105 临时服务器 > 连续失败 3 次后的备份服务器(0x0017) > 主服务器
func (t *Terminal) target(fails int) (string, int, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hostOver != "" {
		return t.hostOver, t.portOver, "指定服务器"
	}
	if bak := strings.TrimSpace(t.Params[0x0017]); bak != "" && fails >= 3 && fails%2 == 1 {
		host, port := bak, t.Cfg.Port
		if i := strings.IndexAny(bak, ";"); i > 0 {
			host = bak[:i]
		}
		if h, p, err := net.SplitHostPort(host); err == nil {
			host = h
			port, _ = strconv.Atoi(p)
		} else if p := t.Params.Int(0x0018); p > 0 {
			port = p
		}
		return host, port, "备份服务器"
	}
	return t.Cfg.Host, t.Cfg.Port, "主服务器"
}

func (t *Terminal) runOnce(stop chan struct{}, fails int) error {
	host, port, label := t.target(fails)
	t.setStatus("connecting", "")
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	t.mu.Lock()
	proto := t.Cfg.transport()
	t.mu.Unlock()
	c, err := net.DialTimeout(proto, addr, 10*time.Second)
	if err != nil {
		t.setStatus("connecting", err.Error())
		return fmt.Errorf("连接%s %s/%s 失败: %v", label, addr, strings.ToUpper(proto), err)
	}
	t.mu.Lock()
	t.conn = c
	t.mu.Unlock()
	readDone := make(chan error, 1)
	go func() {
		err := t.readLoop(c)
		t.failPending() // 断线时让等待应答的请求立即失败
		readDone <- err
	}()
	verNote := t.Cfg.Version
	if t.Cfg.yue() {
		verNote += "  主动安全=粤标"
		if t.Cfg.Version == "2013" {
			verNote += "（粤标基于 JT/T 808-2019，强制按 2019 帧格式）"
		}
	}
	t.logf("已连接%s %s/%s  phone=%s  版本=%s", label, addr, strings.ToUpper(proto), t.Cfg.Phone, verNote)
	defer t.closeConn(c)

	// 登录阶段也要按超时重传（注册/鉴权）
	loginDone := make(chan struct{})
	go func() {
		tk := time.NewTicker(500 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-loginDone:
				return
			case <-tk.C:
				if t.linkTick(false) != nil {
					c.Close()
				}
			}
		}
	}()
	err = t.login()
	close(loginDone)
	if err != nil {
		t.setStatus("connecting", err.Error())
		return err
	}
	t.setStatus("online", "")
	go t.flushBlind()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return nil
		case err := <-readDone:
			return fmt.Errorf("连接断开: %v", err)
		case <-tick.C:
			if err := t.linkTick(true); err != nil {
				return err
			}
		}
	}
}

func (t *Terminal) closeConn(c net.Conn) {
	c.Close()
	t.mu.Lock()
	if t.conn == c {
		t.conn = nil
	}
	t.mu.Unlock()
	t.failPending()
	t.stopStreams()
}

func (t *Terminal) failPending() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, p := range t.pending {
		close(p.done)
		delete(t.pending, k)
	}
}
