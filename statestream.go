package main

// 终端状态推送（SSE）：GET /api/t/{id}/state/stream
// 连接后先推一份完整快照，之后每秒（或页面操作后立即，最快 200ms 一次）比较各顶层字段的 JSON，
// 只推送有变化的字段；每 15 秒发一条注释心跳。页面合并到本地状态后执行渲染钩子，断开时退回 1 秒轮询。
// GET /api/state 仍然可用（脚本与旧页面）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// stateKick 页面操作后立即推送：每个终端一个通知通道，操作完成时关闭并换新
type stateKick struct {
	mu sync.Mutex
	ch chan struct{}
}

func (k *stateKick) wait() <-chan struct{} {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ch == nil {
		k.ch = make(chan struct{})
	}
	return k.ch
}

func (k *stateKick) notify() {
	k.mu.Lock()
	if k.ch != nil {
		close(k.ch)
	}
	k.ch = make(chan struct{})
	k.mu.Unlock()
}

// stateDiff 把状态按顶层字段编码，返回与 prev 相比有变化的字段（prev 为空时返回全部）及新的编码表
func stateDiff(prev map[string][]byte, cur map[string]any) (map[string]json.RawMessage, map[string][]byte) {
	next := make(map[string][]byte, len(cur))
	diff := map[string]json.RawMessage{}
	for k, v := range cur {
		b, err := json.Marshal(v)
		if err != nil {
			continue
		}
		next[k] = b
		if old, ok := prev[k]; !ok || !bytes.Equal(old, b) {
			diff[k] = b
		}
	}
	return diff, next
}

const (
	stateTick      = time.Second
	stateMinGap    = 200 * time.Millisecond
	stateHeartbeat = 15 * time.Second
)

// serveStateStream SSE 推送终端状态。消息：
//
//	event: state  data: {"full":true,"build":"…","state":{…全部字段}}
//	event: state  data: {"diff":{…变化的字段}}
//	event: gone   data: {}            终端已被删除
func serveStateStream(w http.ResponseWriter, r *http.Request, m *Manager, t *Terminal, build string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		fl.Flush()
	}
	diff, prev := stateDiff(nil, t.State())
	send("state", map[string]any{"full": true, "build": build, "state": diff})
	tick := time.NewTicker(stateTick)
	defer tick.Stop()
	beat := time.NewTicker(stateHeartbeat)
	defer beat.Stop()
	last := time.Now()
	for {
		kick := t.kick.wait()
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
			continue
		case <-tick.C:
		case <-kick:
			if wait := stateMinGap - time.Since(last); wait > 0 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(wait):
				}
			}
		}
		if m != nil && m.Get(t.ID) != t {
			send("gone", map[string]any{})
			return
		}
		last = time.Now()
		var d map[string]json.RawMessage
		d, prev = stateDiff(prev, t.State())
		if len(d) > 0 {
			send("state", map[string]any{"diff": d})
		}
	}
}
