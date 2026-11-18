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
	if term.currentImage() != nil {
		t.Fatal("默认应回退合成图")
	}
	if !bytes.Equal(term.attachmentVideo(), alarmMP4) {
		t.Fatal("默认视频应为内置 alarm.mp4")
	}

	// 上传图片：PNG/JPEG 统一重编码为 JPEG 存储
	jpg := makeJPEG(1, 1, 3, time.Now())
	if err := term.setMediaSource("image", ".jpg", jpg); err != nil {
		t.Fatal(err)
	}
	got := term.currentImage() // 上传图片会重编码为 JPEG（质量 90），不能与原始字节比较
	if len(got) < 4 || got[0] != 0xFF || got[1] != 0xD8 {
		t.Fatalf("图片源应作为当前画面（JPEG），得到 %d 字节", len(got))
	}
	saved, err := os.ReadFile(filepath.Join(mediaDir(), "default.jpg")) // 终端无编号（命令行单终端）时为 default
	if err != nil || !bytes.Equal(got, saved) {
		t.Fatalf("当前画面应来自保存的图片文件: %v", err)
	}

	// 上传视频：附件视频用源文件
	video := append([]byte("....ftyp"), make([]byte, 100)...)
	if err := term.setMediaSource("video", ".mp4", video); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(term.attachmentVideo(), video) {
		t.Fatal("视频附件应使用上传的源视频")
	}
	if err := term.setMediaSource("video", ".webm", video); err == nil {
		t.Fatal("非 MP4 应被拒绝")
	}

	// 抓帧优先于文件；帧不按时间作废（窗口最小化时浏览器会节流抓帧，按时间作废会让附件莫名退回合成图）
	frame := append([]byte{0xFF, 0xD8}, make([]byte, 50)...)
	if err := term.setFrame([]byte("bad")); err == nil {
		t.Fatal("非 JPEG 帧应被拒绝")
	}
	if err := term.setFrame(frame); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(term.currentImage(), frame) {
		t.Fatal("应优先使用最新抓帧")
	}
	term.mu.Lock()
	term.frameAt = time.Now().Add(-61 * time.Second) // 很久没有新帧
	mv := term.mediaView()
	term.mu.Unlock()
	if !bytes.Equal(term.currentImage(), frame) {
		t.Fatal("无新帧时应沿用最后一帧，不应退回合成图")
	}
	if mv["stale"] != true {
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
	if err := term.setMediaSource("image", ".jpg", makeJPEG(2, 1, 3, time.Now())); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(term.currentImage(), frame) {
		t.Fatal("切换媒体源后不应再使用旧源的抓帧")
	}

	// 清除后回退默认
	term.clearMediaSource()
	if term.currentImage() != nil || !bytes.Equal(term.attachmentVideo(), alarmMP4) {
		t.Fatal("清除后应回退默认合成画面")
	}
}

// 媒体文件必须支持 Range：<video> 做 seek、未做 faststart 的 MP4 读文件尾 moov 都依赖它
func TestMediaServeRange(t *testing.T) {
	stateDir = t.TempDir()
	m := NewManager(Config{Host: "127.0.0.1", Port: 1, Phone: "13800000001"})
	defer stopAll(m)
	term := m.Get(m.Default())
	video := bytes.Repeat([]byte("x"), 4096)
	if err := term.setMediaSource("video", ".mp4", video); err != nil {
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
}
