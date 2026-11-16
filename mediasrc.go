package main

// 媒体源：报警附件 / 拍照的画面来源。
// 默认是合成的彩条测试图；可改为上传的图片 / 视频文件，或浏览器（桌面版 WebView2 / -web 浏览器）摄像头。
// 视频与摄像头由前端循环播放并每秒 canvas 抓帧回传（POST frame），后端缓存最新一帧；
// 平台 9208 触发附件上传时，图片附件取当前播放画面，视频附件用上传的源视频（摄像头无源视频时回退内置 alarm.mp4）。

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif" // 上传图片支持 GIF 解码
	"image/jpeg"
	_ "image/png" // 上传图片支持 PNG 解码
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func mediaDir() string { return filepath.Join(stateDir, "media") }

const (
	maxMediaBytes = 64 << 20 // 上传媒体文件上限
	maxFrameBytes = 4 << 20  // 单帧 JPEG 上限
	frameFresh    = 10 * time.Second
)

// setMediaSource 上传并设置媒体源（kind = image / video）。图片统一重编码为 JPEG 保存。
func (t *Terminal) setMediaSource(kind, ext string, data []byte) error {
	if kind != "image" && kind != "video" {
		return fmt.Errorf("媒体源类型只能是 image/video")
	}
	if len(data) == 0 {
		return fmt.Errorf("文件为空")
	}
	if kind == "video" && ext != ".mp4" && ext != ".m4v" {
		return fmt.Errorf("视频附件按协议以 .mp4 命名，请上传 MP4 文件")
	}
	id := t.ID
	if id == "" {
		id = "default"
	}
	if err := os.MkdirAll(mediaDir(), 0o755); err != nil {
		return err
	}
	var out []byte
	if kind == "image" {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("图片解码失败（支持 JPEG/PNG/GIF）: %v", err)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			return err
		}
		out, ext = buf.Bytes(), ".jpg"
	} else {
		out = data
	}
	path := filepath.Join(mediaDir(), id+ext)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// 清掉旧源的其它扩展名文件
	for _, e := range []string{".jpg", ".mp4"} {
		if e != ext {
			os.Remove(filepath.Join(mediaDir(), id+e))
		}
	}
	t.mu.Lock()
	t.Cfg.MediaSrc = kind
	t.markDirty()
	t.mu.Unlock()
	t.logf("媒体源已设置为%s（%d 字节）", map[string]string{"image": "图片文件", "video": "视频文件"}[kind], len(out))
	return nil
}

// clearMediaSource 恢复默认合成画面
func (t *Terminal) clearMediaSource() {
	id := t.ID
	if id == "" {
		id = "default"
	}
	for _, e := range []string{".jpg", ".mp4"} {
		os.Remove(filepath.Join(mediaDir(), id+e))
	}
	t.mu.Lock()
	t.Cfg.MediaSrc, t.frame, t.frameAt = "", nil, time.Time{}
	t.markDirty()
	t.mu.Unlock()
	t.logf("媒体源已恢复为默认合成画面")
}

// setCamera 记录/取消摄像头模式（视频流与抓帧都在前端）
func (t *Terminal) setCamera(on bool) {
	t.mu.Lock()
	if on {
		t.Cfg.MediaSrc = "camera"
	} else if t.Cfg.MediaSrc == "camera" {
		t.Cfg.MediaSrc = ""
		t.frame, t.frameAt = nil, time.Time{}
	}
	t.markDirty()
	t.mu.Unlock()
}

// setFrame 前端回传的当前画面（JPEG）
func (t *Terminal) setFrame(data []byte) error {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return fmt.Errorf("不是 JPEG 数据")
	}
	t.mu.Lock()
	t.frame, t.frameAt = data, time.Now()
	t.mu.Unlock()
	return nil
}

// currentImage 当前画面：优先最新抓帧（10 秒内），其次上传的图片文件；都没有返回 nil（调用方回退合成图）
func (t *Terminal) currentImage() []byte {
	t.mu.Lock()
	f, at, src, id := t.frame, t.frameAt, t.Cfg.MediaSrc, t.ID
	t.mu.Unlock()
	if len(f) > 0 && time.Since(at) < frameFresh {
		return f
	}
	if src == "image" {
		if id == "" {
			id = "default"
		}
		if b, err := os.ReadFile(filepath.Join(mediaDir(), id+".jpg")); err == nil {
			return b
		}
	}
	return nil
}

// attachmentVideo 视频附件内容：上传的源视频优先，否则内置 alarm.mp4
func (t *Terminal) attachmentVideo() []byte {
	t.mu.Lock()
	src, id := t.Cfg.MediaSrc, t.ID
	t.mu.Unlock()
	if src == "video" {
		if id == "" {
			id = "default"
		}
		if b, err := os.ReadFile(filepath.Join(mediaDir(), id+".mp4")); err == nil {
			return b
		}
	}
	return alarmMP4
}

// mediaView 媒体源状态（页面显示用）。调用方持有 t.mu（State 内调用）
func (t *Terminal) mediaView() map[string]any {
	age := -1
	if !t.frameAt.IsZero() {
		age = int(time.Since(t.frameAt).Seconds())
	}
	return map[string]any{"src": t.Cfg.MediaSrc, "frameAge": age, "frameSize": len(t.frame)}
}

// registerMediaSrcAPI 终端级媒体源与抓帧接口
func registerMediaSrcAPI(handle func(string, termHandler)) {
	handle("POST /mediasrc", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		if r.URL.Query().Get("kind") == "camera" { // 摄像头：无文件，前端 getUserMedia 成功后标记
			t.setCamera(true)
			writeJSON(w, 200, map[string]any{"ok": true})
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMediaBytes))
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "读取失败: " + err.Error()})
			return
		}
		if err := t.setMediaSource(r.URL.Query().Get("kind"), r.URL.Query().Get("ext"), data); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	handle("GET /mediasrc", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		t.mu.Lock()
		src, id := t.Cfg.MediaSrc, t.ID
		t.mu.Unlock()
		if id == "" {
			id = "default"
		}
		ext := map[string]string{"image": ".jpg", "video": ".mp4"}[src]
		if ext == "" {
			writeJSON(w, 404, map[string]any{"ok": false, "error": "未上传媒体文件"})
			return
		}
		b, err := os.ReadFile(filepath.Join(mediaDir(), id+ext))
		if err != nil {
			writeJSON(w, 404, map[string]any{"ok": false, "error": "媒体文件不存在"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", map[string]string{".jpg": "image/jpeg", ".mp4": "video/mp4"}[ext])
		w.Write(b)
	})
	handle("DELETE /mediasrc", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		t.clearMediaSource()
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	handle("POST /frame", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFrameBytes))
		if err != nil || t.setFrame(data) != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "无效帧数据"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
}
