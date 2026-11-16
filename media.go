package main

// 多媒体生成：真实 JPEG（彩条测试图，按通道/时间变化）、WAV（8kHz 正弦音），按条目确定性重建，不落盘

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"time"
)

var resolutions = map[int][2]int{1: {320, 240}, 2: {640, 480}, 3: {800, 600}, 4: {1024, 768}, 5: {176, 144},
	6: {352, 288}, 7: {704, 288}, 8: {704, 576}}

var mediaTypeName = map[int]string{0: "图像", 1: "音频", 2: "视频"}

var mediaEventName = map[int]string{0: "平台指令", 1: "定时动作", 2: "抢劫报警", 3: "碰撞侧翻", 4: "定距动作",
	5: "门开拍照", 6: "门关拍照", 7: "车门由开变关"}

func makeJPEG(channel, res, quality int, at time.Time) []byte {
	wh, ok := resolutions[res]
	if !ok {
		wh = resolutions[1]
	}
	w, h := wh[0], wh[1]
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bars := []color.RGBA{{235, 235, 235, 255}, {235, 235, 16, 255}, {16, 235, 235, 255}, {16, 235, 16, 255},
		{235, 16, 235, 255}, {235, 16, 16, 255}, {16, 16, 235, 255}}
	sec := at.Second()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := bars[x*len(bars)/w]
			if y > h*3/4 { // 底部灰阶
				g := uint8(x * 255 / w)
				c = color.RGBA{g, g, g, 255}
			}
			img.Set(x, y, c)
		}
	}
	// 方块位置随秒变化，边框颜色按通道区分，便于肉眼区分不同照片
	bx := (sec * w / 60) % (w - w/8)
	border := bars[channel%len(bars)]
	for y := h/2 - h/16; y < h/2+h/16; y++ {
		for x := bx; x < bx+w/8; x++ {
			img.Set(x, y, color.RGBA{0, 0, 0, 255})
		}
	}
	for x := 0; x < w; x++ {
		for d := 0; d < 4; d++ {
			img.Set(x, d, border)
			img.Set(x, h-1-d, border)
		}
	}
	q := 100 - (max(min(quality, 10), 1)-1)*9
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: q})
	return buf.Bytes()
}

// makeWAV 8kHz 8bit 单声道 440Hz，最长 10 秒
func makeWAV(sec int) []byte {
	sec = max(min(sec, 10), 1)
	n := 8000 * sec
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(128 + 60*math.Sin(2*math.Pi*440*float64(i)/8000))
	}
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + n))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(1))
	w(uint32(8000))
	w(uint32(8000))
	w(uint16(1))
	w(uint16(8))
	b.WriteString("data")
	w(uint32(n))
	b.Write(data)
	return b.Bytes()
}

// mediaData 按条目重建多媒体数据
func mediaData(it MediaItem) []byte {
	switch it.Type {
	case 1:
		return makeWAV(it.Dur)
	case 2:
		if clip := videoClip(); len(clip) > 0 {
			return clip
		}
	}
	return makeJPEG(it.Channel, it.Res, it.Quality, time.UnixMilli(it.T))
}

// newMedia 登记一条多媒体（含拍摄时位置）。调用方持有 t.mu
func (t *Terminal) newMedia(typ, channel, event, res, quality, dur int) MediaItem {
	t.MediaSeq++
	it := MediaItem{ID: t.MediaSeq, Type: typ, Channel: channel, Event: event, T: time.Now().UnixMilli(),
		Res: res, Quality: quality, Dur: dur, Loc: hex.EncodeToString(t.locBody(nil)[:28])}
	it.Format = map[int]int{0: 0, 1: 3, 2: 4}[typ] // JPEG / WAV / 视频
	it.Size = len(mediaData(it))
	t.Media = append(t.Media, it)
	if len(t.Media) > 200 {
		t.Media = t.Media[len(t.Media)-200:]
	}
	t.markDirty()
	return it
}

// uploadMedia 0800 多媒体事件 + 0801 多媒体数据（自动分包，平台 8800 可要求重传）
func (t *Terminal) uploadMedia(it MediaItem) error {
	head := cat(u32(it.ID), []byte{byte(it.Type), byte(it.Format), byte(it.Event), byte(it.Channel)})
	desc := mediaTypeName[it.Type] + " " + mediaEventName[it.Event] + " 通道" + itoa(it.Channel)
	if _, _, err := t.send(0x0800, head, desc); err != nil {
		return err
	}
	loc, _ := hex.DecodeString(it.Loc)
	if len(loc) != 28 {
		loc = make([]byte, 28)
	}
	data := mediaData(it)
	_, _, err := t.send(0x0801, cat(head, loc, data), desc+" "+itoa(len(data))+" 字节")
	return err
}
