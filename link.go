package main

// 链路层：发送 / 应答确认与超时重传（参数 0x0002/0x0003）/ 分包上行缓存 / 平台应答等待

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

type pend struct {
	id, serial int
	frame      []byte
	deadline   time.Time
	tries      int
	done       chan int // 平台通用应答结果
}

var errOffline = errors.New("未连接平台")

// ackable 平台会以 8001 应答的上行（网关对无专用应答的消息统一回 8001）
func ackable(id int) bool {
	switch id {
	case 0x0001, 0x0100, 0x0801, 0x0104, 0x0107, 0x0201, 0x0302, 0x0500, 0x0608, 0x0700, 0x0802, 0x0805, 0x1003, 0x1205:
		return false
	}
	return true
}

// ackTimeout 第 try 次发送（0 = 首发）后的应答超时。调用方持有 t.mu
// JT/T 808 4.4.2：T(n+1) = T(n) × (n+1)，T0 = 0x0002（UDP 为 0x0004），即 T0、T0、2T0、6T0、24T0 …（T0 × try!）
func (t *Terminal) ackTimeout(try int) time.Duration {
	id := 0x0002
	if t.Cfg.transport() == "udp" {
		id = 0x0004
	}
	return retransTimeout(time.Duration(max(t.Params.Int(id), 1))*time.Second, try)
}

// maxRetries 重传次数：TCP 用 0x0003，UDP 用 0x0005。调用方持有 t.mu
func (t *Terminal) maxRetries() int {
	if t.Cfg.transport() == "udp" {
		return max(t.Params.Int(0x0005), 0)
	}
	return max(t.Params.Int(0x0003), 0)
}

func retransTimeout(t0 time.Duration, try int) time.Duration {
	d := t0
	for n := 1; n <= try; n++ {
		d *= time.Duration(n)
		if d > 24*time.Hour { // 防溢出：重传次数被设得很大时封顶
			return 24 * time.Hour
		}
	}
	return d
}

func (t *Terminal) allocSerial(n int) int {
	first := (t.serial + 1) & 0xFFFF
	t.serial = (t.serial + n) & 0xFFFF
	return first
}

func (t *Terminal) writeFrame(f []byte) error {
	t.mu.Lock()
	c := t.conn
	t.mu.Unlock()
	if c == nil {
		return errOffline
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.Write(f)
	return err
}

// send 发送上行报文；需要平台应答的登记重传。返回首包流水号与应答通道（不需要应答时为 nil）
func (t *Terminal) send(id int, body []byte, summary string) (int, chan int, error) {
	first, done, _, err := t.sendX(id, body, summary, 0)
	return first, done, err
}

// sendX replyID>0 时在分配流水号的同时登记专用应答等待（如 0100 -> 8100），避免应答先于登记到达
func (t *Terminal) sendX(id int, body []byte, summary string, replyID int) (int, chan int, chan *Msg, error) {
	t.mu.Lock()
	if t.conn == nil {
		t.mu.Unlock()
		return 0, nil, nil, errOffline
	}
	ver, phone := t.Cfg.ver(), t.Cfg.Phone
	body, enc := t.encryptUp(id, body)
	if enc {
		summary = "[RSA] " + summary
	}
	var frames [][]byte
	var first int
	if len(body) <= 1000 {
		first = t.allocSerial(1)
		frames = [][]byte{FrameEnc(id, phone, first, body, ver, 0, 0, enc)}
	} else {
		n := (len(body) + 999) / 1000
		first = t.allocSerial(n)
		for i := 0; i < n; i++ {
			frames = append(frames, FrameEnc(id, phone, (first+i)&0xFFFF, body[i*1000:min((i+1)*1000, len(body))], ver, n, i+1, enc))
		}
		t.cacheUp(first, frames)
	}
	var reply chan *Msg
	if replyID > 0 {
		reply = make(chan *Msg, 1)
		t.waiters[waitKey(replyID, first)] = reply
	}
	var done chan int
	if len(frames) == 1 && ackable(id) {
		done = make(chan int, 1)
		t.pending[first] = &pend{id: id, serial: first, frame: frames[0], deadline: time.Now().Add(t.ackTimeout(0)), done: done}
	}
	t.counters.Up += len(frames)
	t.lastUp = time.Now()
	t.mu.Unlock()
	for i, f := range frames {
		if err := t.writeFrame(f); err != nil {
			t.dropPending(first)
			return first, nil, reply, err
		}
		if len(frames) > 1 && i > 0 && i < len(frames)-1 {
			continue // 分包只记首尾两条日志
		}
		s := summary
		if len(frames) > 1 {
			s = fmt.Sprintf("[分包 %d/%d 共 %d 字节] %s", i+1, len(frames), len(body), summary)
		}
		t.logMsg("up", id, (first+i)&0xFFFF, len(body), f, s)
	}
	return first, done, reply, nil
}

func waitKey(replyID, serial int) string { return fmt.Sprintf("%04X:%d", replyID, serial) }

func (t *Terminal) dropPending(serial int) {
	t.mu.Lock()
	delete(t.pending, serial)
	t.mu.Unlock()
}

// sendWait 发送并等待平台通用应答，返回结果码（超时重传全部失败返回 error）
func (t *Terminal) sendWait(id int, body []byte, summary string) (int, error) {
	_, done, err := t.send(id, body, summary)
	if err != nil || done == nil {
		return 0, err
	}
	r, ok := <-done
	if !ok {
		return 0, fmt.Errorf("%04X 未收到平台应答", id)
	}
	return r, nil
}

func (t *Terminal) cacheUp(first int, frames [][]byte) {
	t.upCache[first] = frames
	t.upOrder = append(t.upOrder, first)
	if len(t.upOrder) > 16 {
		delete(t.upCache, t.upOrder[0])
		t.upOrder = t.upOrder[1:]
	}
}

// resend 补传分包（8003 / 8800）；ids 为包序号（从 1 开始），为空表示全部
func (t *Terminal) resend(first int, ids []int) int {
	t.mu.Lock()
	frames := t.upCache[first]
	t.mu.Unlock()
	if frames == nil {
		return 0
	}
	if len(ids) == 0 {
		for i := range frames {
			ids = append(ids, i+1)
		}
	}
	n := 0
	for _, i := range ids {
		if i >= 1 && i <= len(frames) && t.writeFrame(frames[i-1]) == nil {
			n++
		}
	}
	t.mu.Lock()
	t.counters.Retrans += n
	t.mu.Unlock()
	return n
}

// findUpByMedia 按多媒体 ID 找分包缓存（0801 包体前 4 字节为多媒体 ID）
func (t *Terminal) findUpByMedia(mediaID int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.upOrder) - 1; i >= 0; i-- {
		fs := t.upCache[t.upOrder[i]]
		if m := Parse(fs[0][1 : len(fs[0])-1]); m != nil && m.ID == 0x0801 && len(m.Body) >= 4 &&
			int(binary.BigEndian.Uint32(m.Body)) == mediaID {
			return t.upOrder[i]
		}
	}
	return -1
}
