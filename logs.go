package main

// 结构化报文日志：控制台 + Web（SSE）共用

import (
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type LogEntry struct {
	Seq     int64  `json:"seq"`
	T       int64  `json:"t"`   // 毫秒时间戳
	Dir     string `json:"dir"` // up / down / sys / err
	MsgID   string `json:"msgId"`
	Name    string `json:"name"`
	Serial  int    `json:"serial"`
	Len     int    `json:"len"`
	Summary string `json:"summary"`
	Hex     string `json:"hex"`
}

const logKeep = 2000

type logHub struct {
	mu     sync.Mutex
	seq    int64
	ring   []LogEntry
	subs   map[chan LogEntry]bool
	prefix string // 控制台输出前缀（多终端时区分终端，如 "#2 13800000002"）
}

func newHub() *logHub { return &logHub{subs: map[chan LogEntry]bool{}} }

// hub 进程级（系统）日志；每个终端另有自己的 hub（Terminal.hub）
var hub = newHub()

// Quiet 为 true 时不在控制台打印心跳等高频报文
var Quiet = true

func (h *logHub) add(e LogEntry) {
	h.mu.Lock()
	h.seq++
	e.Seq = h.seq
	if e.T == 0 {
		e.T = time.Now().UnixMilli()
	}
	pre := h.prefix
	h.ring = append(h.ring, e)
	if len(h.ring) > logKeep {
		h.ring = h.ring[len(h.ring)-logKeep:]
	}
	for ch := range h.subs {
		select {
		case ch <- e:
		default: // 慢消费者丢弃
		}
	}
	h.mu.Unlock()
	if !(Quiet && (e.MsgID == "0002" || e.MsgID == "8001" && e.Summary == "0002 成功")) {
		if pre != "" {
			fmt.Println("[" + pre + "] " + e.line())
		} else {
			fmt.Println(e.line())
		}
	}
}

func (h *logHub) sub() (chan LogEntry, []LogEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan LogEntry, 256)
	h.subs[ch] = true
	return ch, append([]LogEntry(nil), h.ring...)
}

func (h *logHub) unsub(ch chan LogEntry) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (e LogEntry) line() string {
	ts := time.UnixMilli(e.T).Format("15:04:05")
	switch e.Dir {
	case "up":
		return fmt.Sprintf("[%s] >> %s %s serial=%d len=%d  %s", ts, e.MsgID, e.Name, e.Serial, e.Len, e.Summary)
	case "down":
		return fmt.Sprintf("[%s] << %s %s serial=%d len=%d  %s", ts, e.MsgID, e.Name, e.Serial, e.Len, e.Summary)
	case "err":
		return fmt.Sprintf("[%s] !! %s", ts, e.Summary)
	}
	return fmt.Sprintf("[%s] %s", ts, e.Summary)
}

func logf(format string, a ...any) { hub.add(LogEntry{Dir: "sys", Summary: fmt.Sprintf(format, a...)}) }

func errf(format string, a ...any) { hub.add(LogEntry{Dir: "err", Summary: fmt.Sprintf(format, a...)}) }

func (t *Terminal) logHub() *logHub {
	if t == nil || t.hub == nil {
		return hub
	}
	return t.hub
}

func (t *Terminal) logf(format string, a ...any) {
	t.logHub().add(LogEntry{Dir: "sys", Summary: fmt.Sprintf(format, a...)})
}

func (t *Terminal) errf(format string, a ...any) {
	t.logHub().add(LogEntry{Dir: "err", Summary: fmt.Sprintf(format, a...)})
}

// logMsg 记录一条上/下行报文；frame 为完整帧（转义后）
func (t *Terminal) logMsg(dir string, id, serial, bodyLen int, frame []byte, summary string) {
	hx := hex.EncodeToString(frame)
	if len(hx) > 8192 {
		hx = hx[:8192] + "..."
	}
	t.logHub().add(LogEntry{Dir: dir, MsgID: fmt.Sprintf("%04X", id), Name: msgName(id), Serial: serial,
		Len: bodyLen, Summary: summary, Hex: hx})
}

// out 纯文本（帮助等），打印到控制台并作为 sys 日志
func (t *Terminal) out(s string) { t.logHub().add(LogEntry{Dir: "sys", Summary: s}) }
