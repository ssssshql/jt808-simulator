package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http/httptest"
	"testing"
	"time"
)

// 纯色图便于统计水印白字像素（彩条图自带亮色，不适合做白像素判定）
func solidJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{0, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	return buf.Bytes()
}

func whitePixels(t *testing.T, data []byte) (int, image.Image) {
	t.Helper()
	im, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("应仍是合法 JPEG: %v", err)
	}
	n := 0
	b := im.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := im.At(x, y).RGBA()
			if r > 0xE000 && g > 0xE000 && bl > 0xE000 {
				n++
			}
		}
	}
	return n, im
}

// 时间水印：烧入后仍是合法 JPEG，黑底上出现白字；不同通道/时间文字不同；坏数据原样返回
func TestStampJPEG(t *testing.T) {
	base := solidJPEG(320, 240)
	if n, _ := whitePixels(t, base); n != 0 {
		t.Fatalf("原图应没有白像素，得到 %d", n)
	}
	stamped := stampJPEG(base, 64, time.Now())
	if bytes.Equal(base, stamped) {
		t.Fatal("烧入水印后字节应有变化")
	}
	if n, _ := whitePixels(t, stamped); n < 50 {
		t.Fatalf("水印应有白字像素（左下角），得到 %d", n)
	}

	// 通道号参与水印文字
	at := time.Now()
	if bytes.Equal(stampJPEG(base, 64, at), stampJPEG(base, 65, at)) {
		t.Fatal("不同通道的水印应不同")
	}
	// 时间参与水印文字（跨秒）
	if bytes.Equal(stampJPEG(base, 64, at), stampJPEG(base, 64, at.Add(2*time.Second))) {
		t.Fatal("不同时间的水印应不同")
	}

	// 无法解码 / 过小的图：不 panic、原样返回
	bad := []byte{0xFF, 0xD8, 0x01, 0x02}
	if got := stampJPEG(bad, 64, time.Now()); !bytes.Equal(got, bad) {
		t.Fatal("无法解码的数据应原样返回")
	}
	if got := stampJPEG(solidJPEG(40, 20), 64, time.Now()); len(got) == 0 {
		t.Fatal("小图不应返回空数据")
	}
}

// 水印开关：默认开启（附件图片带水印），Config.NoStamp 关闭后与原始画面一致
func TestStampToggle(t *testing.T) {
	stateDir = t.TempDir()
	term := NewTerminal(Config{Phone: "13800000000"})
	frame := solidJPEG(320, 240)
	term.setCamera(64, true)
	if err := term.setFrame(64, frame); err != nil {
		t.Fatal(err)
	}
	if !term.stampEnabled() {
		t.Fatal("默认应开启时间水印")
	}
	if on := term.attachmentImage(64); bytes.Equal(on, frame) {
		t.Fatal("默认应烧入水印")
	}
	term.mu.Lock()
	term.Cfg.NoStamp = true
	term.mu.Unlock()
	if term.stampEnabled() {
		t.Fatal("NoStamp 应关闭水印")
	}
	if off := term.attachmentImage(64); !bytes.Equal(off, frame) {
		t.Fatal("关闭水印后应返回原始帧")
	}
}

// 开关接口 + 预览链路：切换后 State 反映状态，预览图随之变化
func TestStampAPI(t *testing.T) {
	stateDir = t.TempDir()
	m := NewManager(Config{Host: "127.0.0.1", Port: 1, Phone: "13800000001"})
	defer stopAll(m)
	term := m.Get(m.Default())
	if err := term.setMediaSource(0, "image", ".jpg", solidJPEG(320, 240)); err != nil {
		t.Fatal(err)
	}
	h := newWebMux(m)
	post := func(on string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/t/"+term.ID+"/mediasrc/stamp?on="+on, nil))
		if w.Code != 200 {
			t.Fatalf("开关接口返回 %d", w.Code)
		}
	}
	preview := func() []byte {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/t/"+term.ID+"/attach-preview?ch=64", nil))
		if w.Code != 200 {
			t.Fatalf("预览返回 %d", w.Code)
		}
		return w.Body.Bytes()
	}

	// 默认开启：State 报告 mediaStamp=true，预览带水印（与图片文件不同）
	if term.State()["mediaStamp"] != true {
		t.Fatal("默认 State.mediaStamp 应为 true")
	}
	withStamp := preview()
	post("0")
	if term.State()["mediaStamp"] != false || term.stampEnabled() {
		t.Fatal("关闭后 State.mediaStamp 应为 false")
	}
	if noStamp := preview(); bytes.Equal(noStamp, withStamp) {
		t.Fatal("关闭水印后预览图应有变化")
	}
	post("1")
	if !term.stampEnabled() {
		t.Fatal("重新开启后应恢复水印")
	}
}
