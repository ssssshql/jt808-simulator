package main

import (
	"bytes"
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
	if got := term.currentImage(); !bytes.Equal(got, jpg) {
		t.Fatal("图片源应作为当前画面")
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

	// 抓帧优先于文件，且只在 10 秒新鲜期内有效
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
	term.frameAt = time.Now().Add(-11 * time.Second)
	term.mu.Unlock()
	if bytes.Equal(term.currentImage(), frame) {
		t.Fatal("过期抓帧不应使用")
	}

	// 附件列表：图片用当前画面，视频用源视频
	term.setFrame(frame)
	files := term.attachFiles(adasAlarm{0x64, 1, 1}, 2, "NO1")
	if len(files) != 2 || files[0].Type != 0 || files[1].Type != 2 {
		t.Fatalf("附件结构不对: %+v", files)
	}
	if !bytes.Equal(files[0].Data, frame) || !bytes.Equal(files[1].Data, video) {
		t.Fatal("附件内容应来自媒体源")
	}

	// 清除后回退默认
	term.clearMediaSource()
	if term.currentImage() != nil || !bytes.Equal(term.attachmentVideo(), alarmMP4) {
		t.Fatal("清除后应回退默认合成画面")
	}
}
