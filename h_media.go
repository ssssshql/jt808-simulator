package main

// 多媒体下行：8801 拍照/录像 8802 检索 8803 批量上传 8804 录音 8805 单条上传

import (
	"fmt"
	"time"
)

const cameraChannels = 4 // 通道 1~4 有摄像头

func init() {
	handlers[0x8801] = (*Terminal).h8801
	handlers[0x8802] = (*Terminal).h8802
	handlers[0x8803] = (*Terminal).h8803
	handlers[0x8804] = (*Terminal).h8804
	handlers[0x8805] = (*Terminal).h8805
}

// capture 拍一张照片（定时/定距/报警触发用）；store=true 只保存不上传
func (t *Terminal) capture(channel, event int, store bool, res int) {
	t.mu.Lock()
	it := t.newMedia(0, channel, event, max(res, 1), t.Params.Int(0x0070), 0)
	t.mu.Unlock()
	if !store {
		if err := t.uploadMedia(it); err != nil {
			t.errf("多媒体 %d 上传失败: %v", it.ID, err)
		}
	}
}

// h8801 通道 命令(0 停止 0xFFFF 录像 其它=张数) 间隔/录像秒 保存标志 分辨率 质量 亮度 对比度 饱和度 色度
func (t *Terminal) h8801(m *Msg) {
	r := newReader(m.Body)
	ch, cmd, interval, save, res, quality := r.u8(), r.u16(), r.u16(), r.u8(), r.u8(), r.u8()
	if r.err != nil {
		t.send(0x0805, cat(u16(m.Serial), []byte{1}, u16(0)), "参数错误")
		return
	}
	t.mu.Lock()
	yue := t.Cfg.yue()
	t.mu.Unlock()
	if yue && (ch == 0x64 || ch == 0x65) {
		t.gdH8801(m, ch)
		return
	}
	if ch < 1 || ch > cameraChannels {
		t.send(0x0805, cat(u16(m.Serial), []byte{2}, u16(0)), fmt.Sprintf("通道 %d 不支持", ch))
		return
	}
	if cmd == 0 {
		t.send(0x0805, cat(u16(m.Serial), []byte{0}, u16(0)), "停止拍摄")
		return
	}
	var items []MediaItem
	t.mu.Lock()
	if cmd == 0xFFFF {
		items = append(items, t.newMedia(2, ch, 0, res, quality, max(interval, 1)))
	} else {
		for i := 0; i < min(cmd, 5); i++ { // 最多 5 张
			items = append(items, t.newMedia(0, ch, 0, res, quality, 0))
		}
	}
	t.mu.Unlock()
	body := cat(u16(m.Serial), []byte{0}, u16(len(items)))
	for _, it := range items {
		body = cat(body, u32(it.ID))
	}
	kind := map[bool]string{true: "录像", false: "拍照"}[cmd == 0xFFFF]
	t.send(0x0805, body, fmt.Sprintf("通道%d %s %d 条 %s", ch, kind, len(items), map[int]string{0: "实时上传", 1: "保存"}[save]))
	t.addInbox("media", "摄像头"+kind, fmt.Sprintf("通道 %d，%d 条，分辨率 %d，质量 %d", ch, len(items), res, quality), 0, nil)
	if save == 1 {
		return
	}
	for i, it := range items {
		if i > 0 {
			time.Sleep(time.Duration(max(interval, 1)) * time.Second)
		}
		if err := t.uploadMedia(it); err != nil {
			t.errf("多媒体 %d 上传失败: %v", it.ID, err)
			return
		}
	}
}

// searchMedia 按 类型/通道(0=全部)/事件/时间段 检索。调用方持有 t.mu
func (t *Terminal) searchMedia(typ, ch, evt int, from, to time.Time) []MediaItem {
	var out []MediaItem
	for _, it := range t.Media {
		at := time.UnixMilli(it.T)
		if it.Type != typ || ch != 0 && it.Channel != ch || it.Event != evt ||
			!from.IsZero() && at.Before(from) || !to.IsZero() && at.After(to) {
			continue
		}
		out = append(out, it)
	}
	return out
}

func readSearch(r *reader) (int, int, int, time.Time, time.Time) {
	return r.u8(), r.u8(), r.u8(), r.time6(), r.time6()
}

func (t *Terminal) h8802(m *Msg) {
	r := newReader(m.Body)
	typ, ch, evt, from, to := readSearch(r)
	if r.err != nil {
		return
	}
	t.mu.Lock()
	list := t.searchMedia(typ, ch, evt, from, to)
	t.mu.Unlock()
	body := cat(u16(m.Serial), u16(len(list)))
	for _, it := range list {
		loc := hexBytes(it.Loc, 28)
		body = cat(body, u32(it.ID), []byte{byte(it.Type), byte(it.Channel), byte(it.Event)}, loc)
	}
	t.send(0x0802, body, fmt.Sprintf("检索到 %d 条%s", len(list), mediaTypeName[typ]))
}

// h8803 存储多媒体数据上传命令：检索后逐条上传，删除标志=1 上传后删除
func (t *Terminal) h8803(m *Msg) {
	r := newReader(m.Body)
	typ, ch, evt, from, to := readSearch(r)
	del := r.u8()
	if r.err != nil {
		t.ack(m, 2)
		return
	}
	t.mu.Lock()
	list := t.searchMedia(typ, ch, evt, from, to)
	t.mu.Unlock()
	t.ack(m, 0)
	for _, it := range list {
		if err := t.uploadMedia(it); err != nil {
			return
		}
		if del == 1 {
			t.removeMedia(it.ID)
		}
	}
}

func (t *Terminal) h8805(m *Msg) {
	r := newReader(m.Body)
	id, del := r.u32(), r.u8()
	if r.err != nil {
		t.ack(m, 2)
		return
	}
	t.mu.Lock()
	var found *MediaItem
	for i := range t.Media {
		if t.Media[i].ID == id {
			it := t.Media[i]
			found = &it
		}
	}
	t.mu.Unlock()
	if found == nil {
		t.ack(m, 1)
		return
	}
	t.ack(m, 0)
	if t.uploadMedia(*found) == nil && del == 1 {
		t.removeMedia(id)
	}
}

func (t *Terminal) removeMedia(id int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	keep := t.Media[:0]
	for _, it := range t.Media {
		if it.ID != id {
			keep = append(keep, it)
		}
	}
	t.Media = keep
	t.markDirty()
}

// h8804 录音：命令(0 停止 1 开始) 时间(0=一直录) 保存标志(0 实时上传 1 保存) 采样率
func (t *Terminal) h8804(m *Msg) {
	r := newReader(m.Body)
	cmd, sec, save := r.u8(), r.u16(), r.u8()
	if r.err != nil {
		t.ack(m, 2)
		return
	}
	t.ack(m, 0)
	if cmd == 0 {
		return
	}
	dur := sec
	if dur == 0 {
		dur = 10
	}
	t.addInbox("media", "录音", fmt.Sprintf("%d 秒，%s", sec, map[int]string{0: "实时上传", 1: "保存"}[save]), 0, nil)
	time.Sleep(time.Duration(min(dur, 10)) * time.Second)
	t.mu.Lock()
	it := t.newMedia(1, 1, 0, 0, 0, dur)
	t.mu.Unlock()
	if save == 0 {
		t.uploadMedia(it)
	}
}
