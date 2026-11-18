package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaSource(t *testing.T) {
	stateDir = t.TempDir()
	term := NewTerminal(Config{Phone: "13800000000"})

	// 默认：合成图 + 内置视频
	if term.currentImage(0) != nil {
		t.Fatal("默认应回退合成图")
	}
	if !bytes.Equal(term.attachmentVideo(0), alarmMP4) {
		t.Fatal("默认视频应为内置 alarm.mp4")
	}

	// 上传图片：PNG/JPEG 统一重编码为 JPEG 存储
	jpg := makeJPEG(1, 1, 3, time.Now())
	if err := term.setMediaSource(0, "image", ".jpg", jpg); err != nil {
		t.Fatal(err)
	}
	got := term.currentImage(0) // 上传图片会重编码为 JPEG（质量 90），不能与原始字节比较
	if len(got) < 4 || got[0] != 0xFF || got[1] != 0xD8 {
		t.Fatalf("图片源应作为当前画面（JPEG），得到 %d 字节", len(got))
	}
	saved, err := os.ReadFile(filepath.Join(mediaDir(), "default.jpg")) // 终端无编号（命令行单终端）时为 default
	if err != nil || !bytes.Equal(got, saved) {
		t.Fatalf("当前画面应来自保存的图片文件: %v", err)
	}

	// 上传视频：附件视频用源文件
	video := append([]byte("....ftyp"), make([]byte, 100)...)
	if err := term.setMediaSource(0, "video", ".mp4", video); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(term.attachmentVideo(0), video) {
		t.Fatal("视频附件应使用上传的源视频")
	}
	if err := term.setMediaSource(0, "video", ".webm", video); err == nil {
		t.Fatal("非 MP4 应被拒绝")
	}

	// 抓帧优先于文件；帧不按时间作废（窗口最小化时浏览器会节流抓帧，按时间作废会让附件莫名退回合成图）
	frame := append([]byte{0xFF, 0xD8}, make([]byte, 50)...)
	if err := term.setFrame(0, []byte("bad")); err == nil {
		t.Fatal("非 JPEG 帧应被拒绝")
	}
	if err := term.setFrame(0, frame); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(term.currentImage(0), frame) {
		t.Fatal("应优先使用最新抓帧")
	}
	term.mu.Lock()
	fb := term.frames[chKey(0)]
	fb.at = time.Now().Add(-61 * time.Second) // 很久没有新帧
	term.frames[chKey(0)] = fb
	mv := term.mediaView()
	term.mu.Unlock()
	if !bytes.Equal(term.currentImage(0), frame) {
		t.Fatal("无新帧时应沿用最后一帧，不应退回合成图")
	}
	if mv[chKey(0)].(map[string]any)["stale"] != true {
		t.Fatal("超过 30 秒无新帧应标记 stale（页面提示用）")
	}

	// 附件列表：图片用当前画面，视频用源视频
	files := term.attachFiles(adasAlarm{0x64, 1, 1}, 2, "NO1")
	if len(files) != 2 || files[0].Type != 0 || files[1].Type != 2 {
		t.Fatalf("附件结构不对: %+v", files)
	}
	if !bytes.Equal(files[0].Data, frame) || !bytes.Equal(files[1].Data, video) {
		t.Fatal("附件内容应来自媒体源")
	}
	if !bytes.Equal(files[0].Data, term.attachFiles(adasAlarm{0x64, 1, 1}, 3, "NO2")[0].Data) {
		t.Fatal("同一次上报的图片附件应取同一画面")
	}

	// 切换媒体源要作废旧帧，否则新附件仍会用上一个源（视频/摄像头）的画面
	if err := term.setMediaSource(0, "image", ".jpg", makeJPEG(2, 1, 3, time.Now())); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(term.currentImage(0), frame) {
		t.Fatal("切换媒体源后不应再使用旧源的抓帧")
	}

	// 清除后回退默认
	term.clearMediaSource(0)
	if term.currentImage(0) != nil || !bytes.Equal(term.attachmentVideo(0), alarmMP4) {
		t.Fatal("清除后应回退默认合成画面")
	}
}

// 按通道隔离：通道单独设置覆盖「全部通道」，未设置的继承；清除后回退继承
func TestMediaChannel(t *testing.T) {
	stateDir = t.TempDir()
	term := NewTerminal(Config{Phone: "13800000000"})

	globalImg := makeJPEG(3, 1, 3, time.Now())
	if err := term.setMediaSource(0, "image", ".jpg", globalImg); err != nil {
		t.Fatal(err)
	}
	chImg := makeJPEG(4, 1, 3, time.Now())
	if err := term.setMediaSource(64, "image", ".jpg", chImg); err != nil {
		t.Fatal(err)
	}

	// 64 用通道专属文件，65 继承全局
	img64, img65 := term.currentImage(64), term.currentImage(65)
	if bytes.Equal(img64, img65) {
		t.Fatal("通道 64 应使用通道专属图片，而不是全局图片")
	}
	if !bytes.Equal(img65, term.currentImage(0)) {
		t.Fatal("通道 65 未设置，应继承全局图片")
	}
	if _, err := os.Stat(filepath.Join(mediaDir(), "default-ch64.jpg")); err != nil {
		t.Fatalf("通道 64 的图片应按通道命名保存: %v", err)
	}

	// 抓帧也按通道隔离：给 64 抓一帧，65 不受影响
	frame := append([]byte{0xFF, 0xD8}, make([]byte, 20)...)
	term.setCamera(64, true)
	if err := term.setFrame(64, frame); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(term.currentImage(64), frame) {
		t.Fatal("通道 64 应使用本通道抓帧")
	}
	if bytes.Equal(term.currentImage(65), frame) {
		t.Fatal("通道 65 不应受影响")
	}

	// 视频附件同样按通道取
	if !bytes.Equal(term.attachmentVideo(65), alarmMP4) {
		t.Fatal("通道 65 未设视频，应用内置 alarm.mp4")
	}

	// 状态视图：64 独立（own），65 继承（!own）
	term.mu.Lock()
	view := term.mediaView()
	term.mu.Unlock()
	v64, v65 := view[chKey(64)].(map[string]any), view[chKey(65)].(map[string]any)
	if v64["src"] != "camera" || v64["own"] != true {
		t.Fatalf("通道 64 视图不对: %v", v64)
	}
	if v65["src"] != "image" || v65["own"] != false {
		t.Fatalf("通道 65 应继承全局图片设置: %v", v65)
	}

	// 清除通道设置后回退全局
	term.clearMediaSource(64)
	if bytes.Equal(term.currentImage(64), frame) {
		t.Fatal("清除通道后不应再用该通道的帧")
	}
	if _, err := os.Stat(filepath.Join(mediaDir(), "default-ch64.jpg")); !os.IsNotExist(err) {
		t.Fatal("清除通道应删除该通道的媒体文件")
	}

	// 删终端清掉全部通道文件
	if err := term.setMediaSource(65, "image", ".jpg", makeJPEG(5, 1, 3, time.Now())); err != nil {
		t.Fatal(err)
	}
	removeMediaFiles(term.ID)
	for _, f := range []string{"default.jpg", "default-ch64.jpg", "default-ch65.jpg", "default.mp4"} {
		if _, err := os.Stat(filepath.Join(mediaDir(), f)); !os.IsNotExist(err) {
			t.Fatalf("%s 应被删除", f)
		}
	}
}

// 媒体文件必须支持 Range：<video> 做 seek、未做 faststart 的 MP4 读文件尾 moov 都依赖它
func TestMediaServeRange(t *testing.T) {
	stateDir = t.TempDir()
	m := NewManager(Config{Host: "127.0.0.1", Port: 1, Phone: "13800000001"})
	defer stopAll(m)
	term := m.Get(m.Default())
	video := bytes.Repeat([]byte("x"), 4096)
	if err := term.setMediaSource(0, "video", ".mp4", video); err != nil {
		t.Fatal(err)
	}
	h := newWebMux(m)
	url := "/api/t/" + term.ID + "/mediasrc"

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), video) {
		t.Fatalf("整文件请求: code=%d len=%d", w.Code, w.Body.Len())
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("应声明 Accept-Ranges: bytes，得到 %q", got)
	}

	r := httptest.NewRequest("GET", url, nil)
	r.Header.Set("Range", "bytes=100-199")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r)
	if w2.Code != 206 || !bytes.Equal(w2.Body.Bytes(), video[100:200]) {
		t.Fatalf("Range 请求应返回 206 与指定片段: code=%d len=%d", w2.Code, w2.Body.Len())
	}

	// 通道继承全局时也能取到文件
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, httptest.NewRequest("GET", url+"?ch=64", nil))
	if w3.Code != 200 || !bytes.Equal(w3.Body.Bytes(), video) {
		t.Fatalf("通道继承全局设置时应回退全局文件: code=%d", w3.Code)
	}
}

// 附件画面预览：来源标注正确，且与附件实际取用的画面一致
func TestAttachPreview(t *testing.T) {
	stateDir = t.TempDir()
	m := NewManager(Config{Host: "127.0.0.1", Port: 1, Phone: "13800000001"})
	defer stopAll(m)
	term := m.Get(m.Default())
	h := newWebMux(m)
	get := func(ch string) (*httptest.ResponseRecorder, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/t/"+term.ID+"/attach-preview?ch="+ch, nil))
		return w, w.Header().Get("X-JT808-Source")
	}

	// 默认合成图
	if w, src := get("64"); w.Code != 200 || src != "synthetic" || !bytes.HasPrefix(w.Body.Bytes(), []byte{0xFF, 0xD8}) {
		t.Fatalf("默认应为合成图: code=%d src=%q", w.Code, src)
	}

	// 通道 64 设视频并抓帧 → 预览来源为 frame，内容就是该帧
	frame := append([]byte{0xFF, 0xD8}, make([]byte, 30)...)
	if err := term.setMediaSource(64, "video", ".mp4", []byte("....ftyp")); err != nil {
		t.Fatal(err)
	}
	if err := term.setFrame(64, frame); err != nil {
		t.Fatal(err)
	}
	if w, src := get("64"); src != "frame" || !bytes.Equal(w.Body.Bytes(), frame) {
		t.Fatalf("应返回通道 64 的抓帧: src=%q len=%d", src, w.Body.Len())
	}
	// 通道 65 仍是默认合成图
	if _, src := get("65"); src != "synthetic" {
		t.Fatalf("通道 65 应仍是合成图，得到 %q", src)
	}
}
