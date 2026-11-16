package main

// 测试用假平台：自动应答注册/鉴权/通用应答，重组终端分包，供用例下发指令并断言终端应答

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

type fakePlat struct {
	t      *testing.T
	ln     net.Listener
	ver    int
	phone  string
	mu     sync.Mutex
	conn   net.Conn
	serial int
	msgs   chan *Msg
	noAck  map[int]bool // 不自动回 8001 的上行
	conns  int
	parts  map[int][][]byte
	pkts   chan *Msg // 每个分包
}

func newFakePlat(t *testing.T, ver int) *fakePlat {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePlat{t: t, ln: ln, ver: ver, msgs: make(chan *Msg, 1000), noAck: map[int]bool{}, parts: map[int][][]byte{}, pkts: make(chan *Msg, 1000)}
	go p.accept()
	t.Cleanup(func() { ln.Close() })
	return p
}

func (p *fakePlat) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *fakePlat) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		p.conn = c
		p.conns++
		p.mu.Unlock()
		go p.read(c)
	}
}

func (p *fakePlat) read(c net.Conn) {
	var r FrameReader
	buf := make([]byte, 65536)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		for _, m := range r.Feed(buf[:n]) {
			if !m.OK {
				p.t.Errorf("终端帧校验错误 %04X", m.ID)
			}
			p.mu.Lock()
			p.phone = m.Phone
			p.mu.Unlock()
			if m.Total > 1 { // 分包重组
				p.pkts <- m
				p.mu.Lock()
				p.parts[m.ID] = append(p.parts[m.ID], m.Body)
				done := len(p.parts[m.ID]) == m.Total
				var body []byte
				if done {
					for _, b := range p.parts[m.ID] {
						body = append(body, b...)
					}
					delete(p.parts, m.ID)
				}
				p.mu.Unlock()
				if !done {
					continue
				}
				m.Body = body
			}
			switch m.ID {
			case 0x0100:
				p.send(0x8100, cat(u16(m.Serial), []byte{0}, []byte("TOKEN123")))
			case 0x0001, 0x0801:
			default:
				p.mu.Lock()
				skip := p.noAck[m.ID]
				p.mu.Unlock()
				if ackable(m.ID) && !skip {
					p.send(0x8001, cat(u16(m.Serial), u16(m.ID), []byte{0}))
				}
			}
			p.msgs <- m
		}
	}
}

func (p *fakePlat) sendFrame(id int, body []byte, total, index int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.serial++
	if p.conn != nil {
		p.conn.Write(Frame(id, p.phone, p.serial, body, p.ver, total, index))
	}
	return p.serial
}

func (p *fakePlat) send(id int, body []byte) int { return p.sendFrame(id, body, 0, 0) }

// expect 等待指定消息；pred 为 nil 时匹配第一条
func (p *fakePlat) expect(id int, d time.Duration, pred func(*Msg) bool) *Msg {
	p.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m := <-p.msgs:
			if m.ID == id && (pred == nil || pred(m)) {
				return m
			}
		case <-deadline:
			p.t.Fatalf("等待 %04X 超时", id)
			return nil
		}
	}
}

// reply 等待对指定下行流水号的终端应答（0001 或专用应答，均以应答流水号开头）
func (p *fakePlat) reply(id, serial int, d time.Duration) *Msg {
	p.t.Helper()
	return p.expect(id, d, func(m *Msg) bool { return len(m.Body) >= 2 && int(binary.BigEndian.Uint16(m.Body)) == serial })
}

// ackResult 0001 应答结果
func (p *fakePlat) ackResult(serial int) int {
	p.t.Helper()
	m := p.reply(0x0001, serial, 5*time.Second)
	if int(binary.BigEndian.Uint16(m.Body[2:])) == 0 {
		p.t.Fatalf("0001 应答消息 ID 为空")
	}
	return int(m.Body[4])
}

func newTestTerm(t *testing.T, p *fakePlat, ver string) *Terminal {
	t.Helper()
	stateDir = t.TempDir()
	if !Quiet { // 只在需要时写，避免与上个用例遗留的终端协程产生数据竞争
		Quiet = true
	}
	term := NewTerminal(Config{Host: "127.0.0.1", Port: p.port(), Phone: "13800000001", Version: ver})
	term.Params[0x0002] = "1" // 应答超时 1s，加快重传用例
	go term.motionLoop()
	term.Start()
	p.expect(0x0102, 5*time.Second, nil)
	waitFor(t, 3*time.Second, func() bool { return term.State()["conn"].(ConnView).Status == "online" })
	t.Cleanup(func() { term.Stop("测试结束") })
	return term
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("条件未在 %v 内满足", d)
}

func setP(term *Terminal, id int, v string) {
	term.mu.Lock()
	term.Params[id] = v
	term.mu.Unlock()
}

// sendEnc 发送消息体已 RSA 加密的下行（消息体属性 bit10）
func (p *fakePlat) sendEnc(id int, body []byte) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.serial++
	if p.conn != nil {
		p.conn.Write(FrameEnc(id, p.phone, p.serial, body, p.ver, 0, 0, true))
	}
	return p.serial
}
