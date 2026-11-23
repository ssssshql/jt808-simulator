package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestIntercomTwoWay(t *testing.T) {
	p := newFakePlat(t, V2019)
	term := newTestTerm(t, p, "2019")

	ms, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	// 预先写入一段特征 G.711A 麦音频，验证上行优先发真实麦数据
	wantMic := bytes.Repeat([]byte{0x2A}, 320)
	term.pushTalkUp(wantMic)

	chDown, unsub := term.subTalkDown()
	defer unsub()

	upAudio := make(chan []byte, 1)
	go func() {
		c, err := ms.Accept()
		if err != nil {
			return
		}
		defer c.Close()

		// 读一帧终端上行的 1078 音频 RTP 包（2019 版 10 字节 SIM，音频包头 30 字节 + 320 字节负载）
		buf := make([]byte, 350)
		if _, err := io.ReadFull(c, buf); err == nil {
			upAudio <- buf[30:]
		}

		// 平台下行回推一帧 1078 G.711A 音频包（4 字节负载 0xD5 = A-law 静音附近）
		payload := []byte{0xD5, 0xD5, 0x55, 0x55}
		pkt := cat(
			[]byte{0x30, 0x31, 0x63, 0x64, 0x81, 0x86}, // PT=6 | M
			u16(1),
			bcd("13800000001", 10),
			[]byte{1, 0x30}, // ch=1, dtype=3 (audio), sub=0
			u32(0), u32(1000),
			u16(len(payload)),
			payload,
		)
		c.Write(pkt)
		time.Sleep(150 * time.Millisecond)
	}()

	addr := ms.Addr().(*net.TCPAddr)
	body := cat(lstr(addr.IP.String()), u16(addr.Port), u16(0), []byte{1, 2, 0}) // ch=1, dataType=2(双向对讲), stype=0
	if r := p.ackResult(p.send(0x9101, body)); r != 0 {
		t.Fatalf("9101 result=%d", r)
	}

	select {
	case got := <-upAudio:
		if !bytes.Equal(got, wantMic) {
			t.Fatalf("上行音频未采用麦克风数据: got=%X", got[:16])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待终端上行对讲音频超时")
	}

	select {
	case pcm := <-chDown:
		if len(pcm) != 8 {
			t.Fatalf("下行解码 PCM 长度错误: got=%d want=8", len(pcm))
		}
		// 0xD5 在 A-law 解码中对应 +8 或 -8
		s0 := int16(binary.LittleEndian.Uint16(pcm[0:2]))
		if s0 > 32 || s0 < -32 {
			t.Fatalf("A-law 静音解码异常: %d", s0)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待平台下行对讲 PCM 超时")
	}

	if r := p.ackResult(p.send(0x9102, []byte{1, 4, 0})); r != 0 { // 4 = 关闭对讲
		t.Fatalf("9102 关闭对讲 result=%d", r)
	}
}
