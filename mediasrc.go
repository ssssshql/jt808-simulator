package main

// 媒体源：报警附件 / 拍照的画面来源，按通道独立设置（64 ADAS / 65 DSM / 66 TPMS / 67 BSD）。
// 通道未单独设置时继承「全部通道」（通道 0）的设置；两者都没设就是合成的彩条测试图。
// 可改为上传的图片 / 视频文件，或浏览器（桌面版 WebView2 / -web 浏览器）摄像头。
// 视频与摄像头由前端循环播放并每秒 canvas 抓帧回传（POST frame），后端按通道缓存最新一帧；
// 平台 9208 触发附件上传时，图片附件取该通道当前画面，视频附件用该通道上传的源视频
// （摄像头无源视频时回退内置 alarm.mp4）。

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
	"strconv"
	"time"
)

func mediaDir() string { return filepath.Join(stateDir, "media") }

const (
	maxMediaBytes = 64 << 20 // 上传媒体文件上限
	maxFrameBytes = 4 << 20  // 单帧 JPEG 上限
	// frameStale 仅为提示用：浏览器在窗口最小化/页面不可见时会把抓帧定时器节流，
	// 超过这个时长没新帧就在页面上提示，但附件仍沿用最后一帧（不回退合成图）
	frameStale = 30 * time.Second
)

// mediaChs 可单独设置媒体源的通道号；0 表示「全部通道」（默认，其他通道未设置时继承它）
var mediaChs = []int{64, 65, 66, 67}

// frameBuf 某通道最近一次抓帧
type frameBuf struct {
	data []byte
	at   time.Time
}

func chKey(ch int) string { return strconv.Itoa(ch) }

func validCh(ch int) bool {
	if ch == 0 {
		return true
	}
	for _, c := range mediaChs {
		if c == ch {
			return true
		}
	}
	return false
}

// mediaChOf 报警属性 → 媒体通道（0x64 ADAS → 64…）；未知属性返回 0
func mediaChOf(attr int) int {
	if ch, ok := map[int]int{0x64: 64, 0x65: 65, 0x66: 66, 0x67: 67}[attr]; ok {
		return ch
	}
	return 0
}

func chLabel(ch int) string {
	if ch == 0 {
		return "全部"
	}
	return strconv.Itoa(ch)
}

func mediaName(kind string) string {
	return map[string]string{"image": "图片文件", "video": "视频文件", "camera": "电脑摄像头"}[kind]
}

func termID(id string) string {
	if id == "" {
		return "default"
	}
	return id
}

// mediaFile 媒体文件路径：通道专属 <终端>-ch64.jpg，全局（通道 0）<终端>.jpg
func mediaFile(id string, ch int, ext string) string {
	if ch == 0 {
		return filepath.Join(mediaDir(), termID(id)+ext)
	}
	return filepath.Join(mediaDir(), fmt.Sprintf("%s-ch%d%s", termID(id), ch, ext))
}

// readMediaFile 先找通道专属文件，再回退全局文件；都没有返回 nil
func readMediaFile(id string, ch int, ext string) []byte {
	if ch != 0 {
		if b, err := os.ReadFile(mediaFile(id, ch, ext)); err == nil {
			return b
		}
	}
	if b, err := os.ReadFile(mediaFile(id, 0, ext)); err == nil {
		return b
	}
	return nil
}

// setMediaLocked 写入配置。kind 为空表示清除该通道的设置（回退继承）。调用方持有 t.mu
func (t *Terminal) setMediaLocked(ch int, kind string) {
	if ch == 0 {
		t.Cfg.MediaSrc = kind
		return
	}
	if kind == "" {
		delete(t.Cfg.MediaCh, chKey(ch))
		if len(t.Cfg.MediaCh) == 0 {
			t.Cfg.MediaCh = nil
		}
		return
	}
	if t.Cfg.MediaCh == nil {
		t.Cfg.MediaCh = map[string]string{}
	}
	t.Cfg.MediaCh[chKey(ch)] = kind
}

// mediaSrcOf 该通道实际生效的来源类型：通道单独设置优先，否则用全局。调用方持有 t.mu
func (t *Terminal) mediaSrcOf(ch int) string {
	if ch != 0 {
		if s := t.Cfg.MediaCh[chKey(ch)]; s != "" {
			return s
		}
	}
	return t.Cfg.MediaSrc
}

// frameOf 该通道的帧：通道专属优先，回退「全部通道」的帧。调用方持有 t.mu
func (t *Terminal) frameOf(ch int) frameBuf {
	if fb, ok := t.frames[chKey(ch)]; ok && len(fb.data) > 0 {
		return fb
	}
	return t.frames[chKey(0)]
}

// setMediaSource 上传并设置媒体源（kind = image / video）。图片统一重编码为 JPEG 保存。
func (t *Terminal) setMediaSource(ch int, kind, ext string, data []byte) error {
	if !validCh(ch) {
		return fmt.Errorf("通道号只能是 0（全部）或 64/65/66/67")
	}
	if kind != "image" && kind != "video" {
		return fmt.Errorf("媒体源类型只能是 image/video")
	}
	if len(data) == 0 {
		return fmt.Errorf("文件为空")
	}
	if kind == "video" && ext != ".mp4" && ext != ".m4v" {
		return fmt.Errorf("视频附件按协议以 .mp4 命名，请上传 MP4 文件")
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
	path := mediaFile(t.ID, ch, ext)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// 清掉同一通道旧源的其它扩展名文件
	for _, e := range []string{".jpg", ".mp4"} {
		if e != ext {
			os.Remove(mediaFile(t.ID, ch, e))
		}
	}
	t.mu.Lock()
	t.setMediaLocked(ch, kind)
	if ch == 0 {
		t.frames = nil // 全部通道换源：各通道的旧帧都作废
	} else {
		delete(t.frames, chKey(ch))
	}
	t.markDirty()
	t.mu.Unlock()
	t.logf("通道 %s 媒体源已设置为%s（%d 字节）", chLabel(ch), mediaName(kind), len(out))
	return nil
}

// clearMediaSource 恢复默认合成画面（通道 0 为全部通道）
func (t *Terminal) clearMediaSource(ch int) {
	if !validCh(ch) {
		return
	}
	for _, e := range []string{".jpg", ".mp4"} {
		os.Remove(mediaFile(t.ID, ch, e))
	}
	t.mu.Lock()
	t.setMediaLocked(ch, "")
	if ch == 0 {
		t.frames = nil
	} else {
		delete(t.frames, chKey(ch))
	}
	t.markDirty()
	t.mu.Unlock()
	t.logf("通道 %s 媒体源已恢复为默认合成画面", chLabel(ch))
}

// setCamera 记录/取消摄像头模式（视频流与抓帧都在前端）
func (t *Terminal) setCamera(ch int, on bool) {
	if !validCh(ch) {
		return
	}
	t.mu.Lock()
	cur := t.mediaSrcOf(ch)
	switch {
	case on:
		t.setMediaLocked(ch, "camera")
		delete(t.frames, chKey(ch)) // 作废上一个源留下的帧
	case cur == "camera":
		t.setMediaLocked(ch, "")
		delete(t.frames, chKey(ch))
	}
	t.markDirty()
	t.mu.Unlock()
}

// setFrame 前端回传的当前画面（JPEG）
func (t *Terminal) setFrame(ch int, data []byte) error {
	if !validCh(ch) {
		return fmt.Errorf("通道号只能是 0（全部）或 64/65/66/67")
	}
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return fmt.Errorf("不是 JPEG 数据")
	}
	t.mu.Lock()
	if t.frames == nil {
		t.frames = map[string]frameBuf{}
	}
	t.frames[chKey(ch)] = frameBuf{data, time.Now()}
	t.mu.Unlock()
	return nil
}

// currentImage 该通道当前画面：视频/摄像头模式沿用最后一帧（不按时间作废——窗口最小化会让浏览器节流
// 抓帧，按时间作废会莫名其妙退回合成图），其次图片文件（通道专属 → 全局）；都没有返回 nil（调用方回退合成图）
func (t *Terminal) currentImage(ch int) []byte {
	t.mu.Lock()
	src, id, fb := t.mediaSrcOf(ch), t.ID, t.frameOf(ch)
	t.mu.Unlock()
	if len(fb.data) > 0 && (src == "video" || src == "camera") {
		return fb.data
	}
	if src == "image" {
		return readMediaFile(id, ch, ".jpg")
	}
	return nil
}

// attachmentVideo 该通道视频附件内容：上传的源视频优先，否则内置 alarm.mp4
func (t *Terminal) attachmentVideo(ch int) []byte {
	t.mu.Lock()
	src, id := t.mediaSrcOf(ch), t.ID
	t.mu.Unlock()
	if src == "video" {
		if b := readMediaFile(id, ch, ".mp4"); b != nil {
			return b
		}
	}
	return alarmMP4
}

// mediaView 各通道媒体源状态（页面显示用）。调用方持有 t.mu（State 内调用）
func (t *Terminal) mediaView() map[string]any {
	out := make(map[string]any, len(mediaChs)+1)
	for _, ch := range append([]int{0}, mediaChs...) {
		src, fb := t.mediaSrcOf(ch), t.frameOf(ch)
		age := -1
		if !fb.at.IsZero() {
			age = int(time.Since(fb.at).Seconds())
		}
		own := src != ""
		if ch != 0 {
			_, own = t.Cfg.MediaCh[chKey(ch)] // 该通道是否独立设置（否则继承全部通道）
		}
		out[chKey(ch)] = map[string]any{
			"src": src, "own": own,
			"frameAge": age, "frameSize": len(fb.data),
			"stale": age > int(frameStale.Seconds()), // 前端抓帧可能被浏览器节流，仅用于页面提示
		}
	}
	return out
}

// removeMediaFiles 删除终端对应的全部媒体文件（删终端时清理，含各通道）
func removeMediaFiles(id string) {
	for _, e := range []string{".jpg", ".mp4"} {
		os.Remove(mediaFile(id, 0, e))
		for _, ch := range mediaChs {
			os.Remove(mediaFile(id, ch, e))
		}
	}
}

// queryCh 解析 ?ch=，缺省为 0（全部通道）
func queryCh(r *http.Request) (int, bool) {
	s := r.URL.Query().Get("ch")
	if s == "" {
		return 0, true
	}
	ch, err := strconv.Atoi(s)
	if err != nil || !validCh(ch) {
		return 0, false
	}
	return ch, true
}

// registerMediaSrcAPI 终端级媒体源与抓帧接口
func registerMediaSrcAPI(handle func(string, termHandler)) {
	handle("POST /mediasrc", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		ch, ok := queryCh(r)
		if !ok {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "通道号只能是 0（全部）或 64/65/66/67"})
			return
		}
		if r.URL.Query().Get("kind") == "camera" { // 摄像头：无文件，前端 getUserMedia 成功后标记
			t.setCamera(ch, true)
			writeJSON(w, 200, map[string]any{"ok": true})
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMediaBytes))
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "读取失败: " + err.Error()})
			return
		}
		if err := t.setMediaSource(ch, r.URL.Query().Get("kind"), r.URL.Query().Get("ext"), data); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	handle("GET /mediasrc", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		ch, ok := queryCh(r)
		if !ok {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "通道号只能是 0（全部）或 64/65/66/67"})
			return
		}
		t.mu.Lock()
		src, id := t.mediaSrcOf(ch), t.ID
		t.mu.Unlock()
		ext := map[string]string{"image": ".jpg", "video": ".mp4"}[src]
		if ext == "" {
			writeJSON(w, 404, map[string]any{"ok": false, "error": "该通道未设置媒体文件"})
			return
		}
		path := mediaFile(id, ch, ext)
		if _, err := os.Stat(path); err != nil {
			path = mediaFile(id, 0, ext) // 通道继承全局设置时用全局文件
		}
		f, err := os.Open(path)
		if err != nil {
			writeJSON(w, 404, map[string]any{"ok": false, "error": "媒体文件不存在"})
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			writeJSON(w, 500, map[string]any{"ok": false, "error": "读取失败: " + err.Error()})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", map[string]string{".jpg": "image/jpeg", ".mp4": "video/mp4"}[ext])
		// ServeContent 支持 Range：<video> 需要它做 seek，未做 faststart 的 MP4 也要靠它读文件尾的 moov
		http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
	})
	handle("DELETE /mediasrc", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		ch, ok := queryCh(r)
		if !ok {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "通道号只能是 0（全部）或 64/65/66/67"})
			return
		}
		t.clearMediaSource(ch)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	handle("POST /frame", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		ch, ok := queryCh(r)
		if !ok {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "通道号无效"})
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFrameBytes))
		if err != nil || t.setFrame(ch, data) != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "无效帧数据"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	// 预览：该通道此刻会用于附件的画面（响应头 X-JT808-Source 标注来源）
	handle("GET /attach-preview", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		ch, ok := queryCh(r)
		if !ok {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "通道号只能是 0（全部）或 64/65/66/67"})
			return
		}
		t.mu.Lock()
		srcTyp, fb := t.mediaSrcOf(ch), t.frameOf(ch)
		t.mu.Unlock()
		img, source := t.currentImage(ch), "image"
		if len(fb.data) > 0 && (srcTyp == "video" || srcTyp == "camera") {
			source = "frame"
		} else if img == nil {
			jc := ch
			if jc == 0 {
				jc = 64
			}
			img, source = makeJPEG(jc, 2, 3, time.Now()), "synthetic"
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("X-JT808-Source", source)
		w.Header().Set("X-JT808-Channel", strconv.Itoa(ch))
		w.Write(img)
	})
}
