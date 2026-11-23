package main

// JT/T 1078 语音对讲（0x9101 数据类型=2 双向对讲）：
// - 上行：前端麦克风采集 8kHz G.711A 音频通过 POST /intercom/up 入队，pump 优先发送实时语音（无麦时回退 440Hz 测试音）。
// - 下行：readAVDown 解析媒体服务器推来的 1078 RTP 音频包（G.711A/U），解码为 8kHz 16-bit LE PCM 推给 GET /intercom/down 播放。

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type talkState struct {
	mu       sync.Mutex
	upBuf    []byte
	upAt     time.Time
	subs     map[chan []byte]struct{}
	rxFrames atomic.Int64
	txMic    atomic.Int64
}

func (t *Terminal) pushTalkUp(g711a []byte) {
	if len(g711a) == 0 {
		return
	}
	t.talk.mu.Lock()
	t.talk.upBuf = append(t.talk.upBuf, g711a...)
	if len(t.talk.upBuf) > 16000 { // 最多缓冲 2 秒（8KB/s），避免积压延迟
		t.talk.upBuf = t.talk.upBuf[len(t.talk.upBuf)-16000:]
	}
	t.talk.upAt = time.Now()
	t.talk.mu.Unlock()
}

func (t *Terminal) popTalkUp(n int) ([]byte, bool) {
	t.talk.mu.Lock()
	defer t.talk.mu.Unlock()
	if len(t.talk.upBuf) == 0 || time.Since(t.talk.upAt) > 1500*time.Millisecond {
		t.talk.upBuf = t.talk.upBuf[:0]
		return nil, false
	}
	take := min(n, len(t.talk.upBuf))
	out := make([]byte, n)
	copy(out, t.talk.upBuf[:take])
	for i := take; i < n; i++ {
		out[i] = 0xD5 // A-law 静音
	}
	rem := copy(t.talk.upBuf, t.talk.upBuf[take:])
	t.talk.upBuf = t.talk.upBuf[:rem]
	t.talk.txMic.Add(1)
	return out, true
}

func alawToLinear(a byte) int16 {
	a ^= 0x55
	t := int(a&0x0F) << 4
	seg := int(a&0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= uint(seg - 1)
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

func ulawToLinear(u byte) int16 {
	u = ^u
	t := int((u&0x0F)<<3) + 0x84
	t <<= uint((u & 0x70) >> 4)
	if u&0x80 != 0 {
		return int16(0x84 - t)
	}
	return int16(t - 0x84)
}

func (t *Terminal) broadcastTalkDown(pt byte, payload []byte) {
	if len(payload) == 0 {
		return
	}
	var pcm []byte
	switch pt {
	case 6: // G.711A
		pcm = make([]byte, len(payload)*2)
		for i, b := range payload {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(alawToLinear(b)))
		}
	case 7: // G.711U
		pcm = make([]byte, len(payload)*2)
		for i, b := range payload {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(ulawToLinear(b)))
		}
	default:
		return
	}
	t.talk.rxFrames.Add(1)
	t.talk.mu.Lock()
	for ch := range t.talk.subs {
		select {
		case ch <- pcm:
		default:
		}
	}
	t.talk.mu.Unlock()
}

func (t *Terminal) subTalkDown() (chan []byte, func()) {
	ch := make(chan []byte, 64)
	t.talk.mu.Lock()
	if t.talk.subs == nil {
		t.talk.subs = map[chan []byte]struct{}{}
	}
	t.talk.subs[ch] = struct{}{}
	t.talk.mu.Unlock()
	return ch, func() {
		t.talk.mu.Lock()
		delete(t.talk.subs, ch)
		t.talk.mu.Unlock()
	}
}

// parse1078AudioHeader 解析 1078 RTP 包头，返回 (pt, dtype, hdrTotalLen, bodyLen, ok)。
// 兼容 2013（6 字节 SIM）与 2019（10 字节 SIM）两种包头长度。
func parse1078Packet(b []byte, preferSimLen int) (pt byte, dtype, totalLen, hdrLen int, ok bool) {
	if len(b) < 24 || b[0] != 0x30 || b[1] != 0x31 || b[2] != 0x63 || b[3] != 0x64 {
		return 0, 0, 0, 0, false
	}
	altSimLen := 10
	if preferSimLen == 10 {
		altSimLen = 6
	}
	for _, simLen := range []int{preferSimLen, altSimLen} {
		if len(b) < 8+simLen+4 {
			continue
		}
		dt := int(b[8+simLen+1] >> 4)
		if dt > 4 {
			continue
		}
		hl := 8 + simLen + 2
		if dt != 4 {
			hl += 8 // 8 字节时间戳
		}
		if dt <= 2 {
			hl += 4 // I 帧间隔 + 帧间隔
		}
		if len(b) < hl+2 {
			continue
		}
		bl := int(binary.BigEndian.Uint16(b[hl : hl+2]))
		if bl <= 0 || bl > 4096 {
			continue
		}
		return b[5] & 0x7F, dt, hl + 2 + bl, hl + 2, true
	}
	return 0, 0, 0, 0, false
}

// readAVDown 接收平台通过 1078 媒体连接下发的音频流（双向对讲下行语音）。
func (t *Terminal) readAVDown(s *avStream, conn net.Conn) {
	t.mu.Lock()
	preferSim := 6
	if t.Cfg.ver() >= V2019 {
		preferSim = 10
	}
	t.mu.Unlock()
	magic := []byte{0x30, 0x31, 0x63, 0x64}
	tmp := make([]byte, 4096)
	var buf []byte
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for len(buf) >= 24 {
				idx := bytes.Index(buf, magic)
				if idx < 0 {
					if len(buf) > 3 {
						buf = buf[len(buf)-3:]
					}
					break
				}
				if idx > 0 {
					buf = buf[idx:]
				}
				pt, dt, totalLen, hdrLen, ok := parse1078Packet(buf, preferSim)
				if !ok {
					if len(buf) > 40 {
						buf = buf[4:]
						continue
					}
					break
				}
				if len(buf) < totalLen {
					break
				}
				if dt == 3 {
					t.broadcastTalkDown(pt, buf[hdrLen:totalLen])
				}
				buf = buf[totalLen:]
			}
			if len(buf) > 65536 {
				buf = buf[:0]
			}
		}
		if err != nil {
			return
		}
	}
}

func registerIntercomAPI(handle func(string, termHandler)) {
	handle("POST /intercom/up", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil || len(data) == 0 {
			writeJSON(w, 200, map[string]any{"ok": false})
			return
		}
		t.pushTalkUp(data)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	handle("GET /intercom/down", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		ch, unsub := t.subTalkDown()
		defer unsub()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		fl, _ := w.(http.Flusher)
		if fl != nil {
			fl.Flush()
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-t.quit:
				return
			case pcm := <-ch:
				if _, err := w.Write(pcm); err != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			}
		}
	})
}
