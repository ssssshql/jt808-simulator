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

	"github.com/gorilla/websocket"
)

type talkState struct {
	mu               sync.Mutex
	upBuf            []byte
	upAt             time.Time
	subs             map[chan []byte]struct{}
	rxRawBytes       atomic.Int64
	rxFrames         atomic.Int64
	txMic            atomic.Int64
	unsupportedLogAt time.Time
	adpcmPred        int32
	adpcmIdx         int
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

var imaIdxTable = [16]int{-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8}

var imaStepTable = [89]int{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143, 157, 173, 190, 209, 230,
	253, 279, 307, 337, 371, 408, 449, 494, 544, 598, 658, 724, 796, 876, 963,
	1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024, 3327,
	3660, 4026, 4428, 4871, 5358, 5894, 6484, 7132, 7845, 8630, 9493, 10442, 11487,
	12635, 13899, 15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
}

func (t *Terminal) decodeADPCM4(data []byte) []byte {
	t.talk.mu.Lock()
	pred, idx := t.talk.adpcmPred, t.talk.adpcmIdx
	pcm := make([]byte, len(data)*4)
	stepNibble := func(nib byte) int16 {
		step := imaStepTable[idx]
		diff := step >> 3
		if nib&1 != 0 {
			diff += step >> 2
		}
		if nib&2 != 0 {
			diff += step >> 1
		}
		if nib&4 != 0 {
			diff += step
		}
		if nib&8 != 0 {
			pred -= int32(diff)
		} else {
			pred += int32(diff)
		}
		if pred > 32767 {
			pred = 32767
		} else if pred < -32768 {
			pred = -32768
		}
		idx += imaIdxTable[nib&0x0F]
		if idx < 0 {
			idx = 0
		} else if idx > 88 {
			idx = 88
		}
		return int16(pred)
	}
	for i, b := range data {
		s0 := stepNibble(b & 0x0F)
		s1 := stepNibble((b >> 4) & 0x0F)
		binary.LittleEndian.PutUint16(pcm[i*4:], uint16(s0))
		binary.LittleEndian.PutUint16(pcm[i*4+2:], uint16(s1))
	}
	t.talk.adpcmPred, t.talk.adpcmIdx = pred, idx
	t.talk.mu.Unlock()
	return pcm
}

func (t *Terminal) broadcastTalkDown(pt byte, payload []byte) {
	if len(payload) > 4 && payload[0] == 0x00 && payload[1] == 0x01 && int(payload[2])*2 == len(payload)-4 && payload[3] == 0x00 {
		payload = payload[4:] // 兼容海思 4 字节音频帧头 (00 01 <len/2> 00)
	}
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
	case 8: // G.726 / 4-bit ADPCM（每字节 2 个采样）
		pcm = t.decodeADPCM4(payload)
	default:
		if t.talk.unsupportedLogAt.IsZero() || time.Since(t.talk.unsupportedLogAt) > 5*time.Second {
			t.talk.unsupportedLogAt = time.Now()
			t.logf("1078 下行音频格式暂不支持: pt=%d len=%d", pt, len(payload))
		}
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

func isBCDBytes(b []byte) bool {
	for _, v := range b {
		if v>>4 > 9 || v&0x0F > 9 {
			return false
		}
	}
	return true
}

// parse1078Packet 解析 1078 RTP 包头，返回 (pt, dtype, totalLen, hdrLen, simLen, ok)。
// 兼容 2013（6 字节 SIM）与 2019（10 字节 SIM）两种包头长度：
// 优先匹配下一包魔数 "01cd" 命中的长度，其次匹配恰好等于当前缓冲长度的包，避免把 6 字节 SIM 误判为 10 字节 SIM。
func parse1078Packet(b []byte, preferSimLen int) (pt byte, dtype, totalLen, hdrLen, usedSim int, ok bool) {
	if len(b) < 24 || b[0] != 0x30 || b[1] != 0x31 || b[2] != 0x63 || b[3] != 0x64 {
		return 0, 0, 0, 0, 0, false
	}
	altSimLen := 10
	if preferSimLen == 10 {
		altSimLen = 6
	}
	type cand struct {
		pt         byte
		dt         int
		tot, hl    int
		sim        int
		nextMagic  bool
		exactLen   bool
		reasonable bool
	}
	var cands []cand
	for _, simLen := range []int{preferSimLen, altSimLen} {
		if len(b) < 8+simLen+4 || !isBCDBytes(b[8:8+simLen]) {
			continue
		}
		ch := b[8+simLen]
		dt := int(b[8+simLen+1] >> 4)
		sub := int(b[8+simLen+1] & 0x0F)
		if ch == 0 || dt > 4 || sub > 3 {
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
		tot := hl + 2 + bl
		hasNext := len(b) >= tot+4 && b[tot] == 0x30 && b[tot+1] == 0x31 && b[tot+2] == 0x63 && b[tot+3] == 0x64
		if len(b) >= tot+4 && !hasNext {
			continue
		}
		cands = append(cands, cand{
			pt:         b[5] & 0x7F,
			dt:         dt,
			tot:        tot,
			hl:         hl + 2,
			sim:        simLen,
			nextMagic:  hasNext,
			exactLen:   len(b) == tot,
			reasonable: dt != 3 || bl <= 1024,
		})
	}
	for _, want := range []func(cand) bool{
		func(c cand) bool { return c.nextMagic },
		func(c cand) bool { return c.exactLen },
		func(c cand) bool { return c.reasonable },
		func(c cand) bool { return true },
	} {
		for _, c := range cands {
			if want(c) {
				return c.pt, c.dt, c.tot, c.hl, c.sim, true
			}
		}
	}
	return 0, 0, 0, 0, 0, false
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
	loggedFirst := false
	var rawWin, audioWin int
	winAt := time.Now()
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			t.talk.rxRawBytes.Add(int64(n))
			rawWin += n
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
				pt, dt, totalLen, hdrLen, simLen, ok := parse1078Packet(buf, preferSim)
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
				if !loggedFirst {
					loggedFirst = true
					t.logf("1078 下行首包: pt=%d dt=%d sim=%dB body=%dB", pt, dt, simLen, totalLen-hdrLen)
				}
				if dt == 3 {
					audioWin++
					t.broadcastTalkDown(pt, buf[hdrLen:totalLen])
				}
				buf = buf[totalLen:]
			}
			if len(buf) > 65536 {
				buf = buf[:0]
			}
			if time.Since(winAt) >= 3*time.Second {
				if rawWin > 0 && audioWin == 0 {
					t.logf("1078 下行异常: 3秒收到 %dB 但未解析出音频帧（缓冲 %dB hex=%X）", rawWin, len(buf), buf[:min(len(buf), 36)])
				}
				rawWin, audioWin, winAt = 0, 0, time.Now()
			}
		}
		if err != nil {
			s.close()
			return
		}
	}
}

var talkWSUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
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
		if websocket.IsWebSocketUpgrade(r) {
			ws, err := talkWSUpgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer ws.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					if _, _, err := ws.ReadMessage(); err != nil {
						return
					}
				}
			}()
			for {
				select {
				case <-done:
					return
				case <-t.quit:
					return
				case pcm := <-ch:
					if err := ws.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
						return
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
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
