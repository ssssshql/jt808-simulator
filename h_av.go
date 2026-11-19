package main

// JT/T 1078 下行：9003 9101 9102 9105 9201 9202 9205 9206 9207 9301~9306

import (
	"fmt"
	"time"
)

const avChannels = 4

func init() {
	handlers[0x9003] = (*Terminal).h9003
	handlers[0x9101] = (*Terminal).h9101
	handlers[0x9102] = (*Terminal).h9102
	handlers[0x9105] = func(t *Terminal, m *Msg) {
		r := newReader(m.Body)
		ch, loss := r.u8(), r.u8()
		t.logf("平台通知通道 %d 丢包率 %d%%", ch, loss)
		t.ack(m, 0)
	}
	handlers[0x9201] = (*Terminal).h9201
	handlers[0x9202] = (*Terminal).h9202
	handlers[0x9205] = (*Terminal).h9205
	handlers[0x9206] = (*Terminal).h9206
	handlers[0x9207] = (*Terminal).h9207
	for id := 0x9301; id <= 0x9306; id++ {
		handlers[id] = (*Terminal).hPTZ
	}
}

// h9003 音视频属性：G.711A 单声道 8kHz 16bit 帧长 320，支持输出，H.264，4 路音频 4 路视频
func (t *Terminal) h9003(m *Msg) {
	t.send(0x1003, cat([]byte{6, 1, 0, 1}, u16(320), []byte{1, 98, avChannels, avChannels}), "G.711A/H.264 4 路")
}

func (t *Terminal) h9101(m *Msg) {
	r := newReader(m.Body)
	ip := r.lstr()
	tcp, udp, ch, dtype, stype := r.u16(), r.u16(), r.u8(), r.u8(), r.u8()
	if r.err != nil {
		t.errf("9101 格式错误（消息体 %d 字节）: %v", len(m.Body), r.err)
		t.addInbox("av", "实时音视频（格式错误）", fmt.Sprintf("消息体 %d 字节: %X", len(m.Body), m.Body), 0, nil)
		t.ack(m, 2)
		return
	}
	if ip == "" || (tcp == 0 && udp == 0) {
		t.errf("9101 地址或端口无效: ip=%q tcp=%d udp=%d", ip, tcp, udp)
		t.addInbox("av", "实时音视频（参数无效）", fmt.Sprintf("通道 %d 地址=%q TCP=%d UDP=%d", ch, ip, tcp, udp), 0, nil)
		t.ack(m, 2)
		return
	}
	if !t.avChannelOK(ch) {
		t.errf("9101 通道 %d 不支持（仅支持 1~%d，粤标另支持 0x64/0x65/0x67）", ch, avChannels)
		t.addInbox("av", "实时音视频（通道不支持）", fmt.Sprintf("通道 %d -> %s TCP %d UDP %d", ch, ip, tcp, udp), 0, nil)
		t.ack(m, 1)
		return
	}
	t.addInbox("av", "实时音视频", fmt.Sprintf("通道 %d %s %s -> %s TCP %d UDP %d", ch, dataTypeName[dtype],
		map[int]string{0: "主码流", 1: "子码流"}[stype], ip, tcp, udp), 0, nil)
	if err := t.startStream(ip, tcp, udp, ch, dtype, "实时", time.Time{}, time.Time{}, 1); err != nil {
		t.errf("%v", err)
		t.ack(m, 1)
		return
	}
	t.ack(m, 0)
}

// h9102 控制指令：0 关闭 1 切换码流 2 暂停 3 恢复 4 关闭对讲；关闭类型：0 全部 1 只关音频 2 只关视频
func (t *Terminal) h9102(m *Msg) {
	r := newReader(m.Body)
	ch, cmd, closeType := r.u8(), r.u8(), r.u8()
	s := t.stream(ch)
	desc := map[int]string{0: "关闭", 1: "切换码流", 2: "暂停", 3: "恢复", 4: "关闭对讲"}[cmd]
	t.addInbox("av", "音视频控制", fmt.Sprintf("通道 %d %s", ch, desc), 0, nil)
	if s != nil {
		switch cmd {
		case 0:
			switch closeType {
			case 1:
				s.audio.Store(false)
			case 2:
				s.video.Store(false)
			}
			if closeType == 0 || !s.audio.Load() && !s.video.Load() {
				t.stopStream(ch)
			}
		case 2:
			s.paused.Store(true)
		case 3:
			s.paused.Store(false)
		case 4:
			s.audio.Store(false)
		}
	}
	t.ack(m, 0)
}

// resources 模拟的录像文件：每通道按整点切分，每段 1 小时，最多 24 段
func resources(ch int, from, to time.Time) [][3]any {
	now := time.Now()
	if to.IsZero() || to.After(now) {
		to = now
	}
	if from.IsZero() || to.Sub(from) > 24*time.Hour {
		from = to.Add(-24 * time.Hour)
	}
	var out [][3]any
	chs := []int{ch}
	if ch == 0 {
		chs = []int{1, 2, 3, 4}
	}
	for _, c := range chs {
		for s := from.Truncate(time.Hour); s.Before(to) && len(out) < 24*len(chs); s = s.Add(time.Hour) {
			st, en := s, s.Add(time.Hour)
			if st.Before(from) {
				st = from
			}
			if en.After(to) {
				en = to
			}
			if en.Sub(st) >= time.Minute {
				out = append(out, [3]any{c, st, en})
			}
		}
	}
	return out
}

func resourceList(serial, ch int, from, to time.Time, avType int) []byte {
	list := resources(ch, from, to)
	body := cat(u16(serial), u32(len(list)))
	for _, it := range list {
		st, en := it[1].(time.Time), it[2].(time.Time)
		size := int(en.Sub(st).Seconds()) * 32 * 1024 // 约 256kbps
		body = cat(body, []byte{byte(it[0].(int))}, bcdTime(st), bcdTime(en), make([]byte, 8), []byte{byte(avType), 1, 1}, u32(size))
	}
	return body
}

func (t *Terminal) h9205(m *Msg) {
	r := newReader(m.Body)
	ch, from, to := r.u8(), r.time6(), r.time6()
	r.take(8)
	avType := r.u8()
	body := resourceList(m.Serial, ch, from, to, avType)
	t.send(0x1205, body, fmt.Sprintf("通道 %d 录像 %d 段", ch, (len(body)-6)/28))
}

// h9201 回放请求：先回 1205 资源列表，再按开始时间推流
func (t *Terminal) h9201(m *Msg) {
	r := newReader(m.Body)
	ip := r.lstr()
	tcp, udp, ch, avType, _, _, mode, speed := r.u16(), r.u16(), r.u8(), r.u8(), r.u8(), r.u8(), r.u8(), r.u8()
	from, to := r.time6(), r.time6()
	if r.err != nil {
		t.errf("9201 格式错误（消息体 %d 字节）: %v", len(m.Body), r.err)
		t.addInbox("av", "录像回放（格式错误）", fmt.Sprintf("消息体 %d 字节: %X", len(m.Body), m.Body), 0, nil)
		t.ack(m, 2)
		return
	}
	t.send(0x1205, resourceList(m.Serial, ch, from, to, avType), "回放资源")
	if from.IsZero() {
		from = time.Now().Add(-time.Hour)
	}
	mult := map[int]int{1: 1, 2: 2, 3: 4, 4: 8, 5: 16}[speed]
	t.addInbox("av", "录像回放", fmt.Sprintf("通道 %d %s ~ %s 方式 %d 倍数 %d -> %s", ch, from.Format("01-02 15:04:05"),
		to.Format("01-02 15:04:05"), mode, mult, ip), 0, nil)
	dtype := map[int]int{0: 0, 1: 3, 2: 1, 3: 0}[avType]
	if err := t.startStream(ip, tcp, udp, ch, dtype, "回放", from, to, max(mult, 1)); err != nil {
		t.errf("%v", err)
	}
}

// h9202 回放控制：0 开始 1 暂停 2 结束 3 快进 4 关键帧快退 5 拖动 6 关键帧播放
func (t *Terminal) h9202(m *Msg) {
	r := newReader(m.Body)
	ch, cmd, speed := r.u8(), r.u8(), r.u8()
	pos := r.time6()
	s := t.stream(ch)
	t.addInbox("av", "回放控制", fmt.Sprintf("通道 %d %s", ch, map[int]string{0: "开始", 1: "暂停", 2: "结束", 3: "快进",
		4: "关键帧快退", 5: "拖动", 6: "关键帧播放"}[cmd]), 0, nil)
	if s != nil {
		switch cmd {
		case 0, 6:
			s.paused.Store(false)
			s.speed.Store(1)
		case 1:
			s.paused.Store(true)
		case 2:
			t.stopStream(ch)
		case 3, 4:
			s.speed.Store(int32(max(map[int]int{1: 1, 2: 2, 3: 4, 4: 8, 5: 16}[speed], 1)))
		case 5:
			if !pos.IsZero() {
				s.seek.Store(pos.UnixMilli())
			}
		}
	}
	t.ack(m, 0)
}

// hPTZ 云台控制类指令，记录后成功应答
func (t *Terminal) hPTZ(m *Msg) {
	t.addInbox("ptz", msgName(m.ID), fmt.Sprintf("%X", m.Body), 0, nil)
	t.ack(m, 0)
}
