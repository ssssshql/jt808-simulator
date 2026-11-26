package main

// 媒体源：报警附件 / 拍照的画面来源，按通道独立设置（64 ADAS / 65 DSM / 66 TPMS / 67 BSD）。
// 通道未单独设置时继承「全部通道」（通道 0）的设置；两者都没设就是合成的彩条测试图。
// 可改为上传的图片 / 视频文件，或浏览器（桌面版 WebView2）摄像头。
// 视频与摄像头由前端循环播放，每秒 canvas 抓帧回传（POST frame），后端按通道缓存最近 ~30 帧；
// 同时前端用 MediaRecorder 每约 6 秒录一段回传（POST videoclip），后端只留最近一段。
// 平台 9208 触发附件上传时，多张图片按序号从帧历史里取不同时刻（水印时间递减），
// 视频附件用最近一段录制（摄像头/视频模式），其次上传的源视频，最后回退内置 alarm.mp4。

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
	"strings"
	"time"
)

func mediaDir() string { return filepath.Join(stateDir, "media") }

const (
	maxMediaBytes = 64 << 20 // 上传媒体文件上限
	maxFrameBytes = 4 << 20  // 单帧 JPEG 上限
	maxClipBytes  = 16 << 20 // 单段录制视频上限
	maxFrames     = 30       // 每通道保留的抓帧数（前端每秒一帧 → 约 30 秒历史）
	// frameStale 仅为提示用：浏览器在窗口最小化/页面不可见时会把抓帧定时器节流，
	// 超过这个时长没新帧就在页面上提示，但附件仍沿用最后一帧（不回退合成图）
	frameStale = 30 * time.Second
	// attachShotGap 一次报警的多张图片之间的抓拍间隔：第 i 张取 (n-1-i)*gap 之前的画面，
	// 让图片附件的时间水印真正错开（否则多张图完全一样，看不出是连续抓拍）
	attachShotGap = 2 * time.Second
)

// mediaChs 可单独设置媒体源的通道号；0 表示「全部通道」（默认，其他通道未设置时继承它）
var mediaChs = []int{1, 2, 3, 4, 64, 65, 66, 67}

// frameBuf 某通道的一次抓帧
type frameBuf struct {
	data []byte
	at   time.Time
}

// clipBuf 视频/摄像头最近一段录制（前端 MediaRecorder 回传），附件视频用它
type clipBuf struct {
	data []byte
	mime string
	at   time.Time
}

func chKey(ch int) string { return strconv.Itoa(ch) }

func validCh(ch int) bool {
	if ch == 0 || (ch >= 1 && ch <= avChannels) {
		return true
	}
	for _, c := range []int{64, 65, 66, 67} {
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

// dropCache 丢掉该通道的抓帧历史与录制片段（换源/清源后旧画面必须作废）。调用方持有 t.mu
func (t *Terminal) dropCache(ch int) {
	if ch == 0 {
		t.frames, t.clips, t.h264 = map[string][]frameBuf{}, map[string]clipBuf{}, map[string]*h264Ring{}
		return
	}
	delete(t.frames, chKey(ch))
	delete(t.clips, chKey(ch))
	delete(t.h264, chKey(ch))
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

// frameOf 该通道最新帧：通道专属优先，回退「全部通道」的帧。调用方持有 t.mu
func (t *Terminal) frameOf(ch int) frameBuf {
	if fb := t.frames[chKey(ch)]; len(fb) > 0 {
		return fb[0]
	}
	if fb := t.frames[chKey(0)]; len(fb) > 0 {
		return fb[0]
	}
	return frameBuf{}
}

// frameAgo 该通道约 back 之前的帧：取时间最接近的一帧，历史不够就用最旧的那帧。调用方持有 t.mu
func (t *Terminal) frameAgo(ch int, back time.Duration) frameBuf {
	list := t.frames[chKey(ch)]
	if len(list) == 0 {
		list = t.frames[chKey(0)]
	}
	if len(list) == 0 {
		return frameBuf{}
	}
	best, diff := list[0], time.Duration(1<<62)
	for _, fb := range list {
		d := time.Since(fb.at) - back
		if d < 0 {
			d = -d
		}
		if d < diff {
			best, diff = fb, d
		}
	}
	return best
}

// clipOf 该通道最近一段录制视频：通道专属优先，回退「全部通道」。调用方持有 t.mu
func (t *Terminal) clipOf(ch int) clipBuf {
	if c, ok := t.clips[chKey(ch)]; ok && len(c.data) > 0 {
		return c
	}
	return t.clips[chKey(0)]
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
	var gifData []byte
	if kind == "image" {
		img, format, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("图片解码失败（支持 JPEG/PNG/GIF）: %v", err)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			return err
		}
		out, ext = buf.Bytes(), ".jpg"
		if format == "gif" {
			gifData = data
		}
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
	if len(gifData) > 0 {
		gp := mediaFile(t.ID, ch, ".gif")
		if err := os.WriteFile(gp+".tmp", gifData, 0o644); err == nil {
			os.Rename(gp+".tmp", gp)
		}
	} else {
		os.Remove(mediaFile(t.ID, ch, ".gif"))
	}
	// 清掉同一通道旧源的其它扩展名文件
	for _, e := range []string{".jpg", ".mp4"} {
		if e != ext {
			os.Remove(mediaFile(t.ID, ch, e))
		}
	}
	t.mu.Lock()
	t.setMediaLocked(ch, kind)
	t.dropCache(ch)
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
	for _, e := range []string{".jpg", ".gif", ".mp4"} {
		os.Remove(mediaFile(t.ID, ch, e))
	}
	t.mu.Lock()
	t.setMediaLocked(ch, "")
	t.dropCache(ch)
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
		t.dropCache(ch) // 作废上一个源留下的帧与录制片段
	case cur == "camera":
		t.setMediaLocked(ch, "")
		t.dropCache(ch)
	}
	t.markDirty()
	t.mu.Unlock()
}

type h264Buf struct {
	data []byte
	key  bool
	seq  uint64
	at   time.Time
}

type h264Ring struct {
	seq   uint64
	sps   []byte
	pps   []byte
	items []h264Buf
}

// splitAnnexBNALUs 将 Annex-B 码流拆分为不含起始码的 NALU 切片（兼容 3/4 字节起始码）。
func splitAnnexBNALUs(b []byte) [][]byte {
	var nalus [][]byte
	for {
		i := bytes.Index(b, []byte{0, 0, 1})
		if i < 0 {
			break
		}
		start := i + 3
		j := bytes.Index(b[start:], []byte{0, 0, 1})
		end := len(b)
		if j >= 0 {
			end = start + j
			if end > start && b[end-1] == 0 {
				end--
			}
		}
		if end > start {
			nalus = append(nalus, b[start:end])
		}
		if j < 0 {
			break
		}
		b = b[end:]
	}
	return nalus
}

// normalizeH264Frame 提取并缓存 SPS/PPS，确保每个关键帧都自带 SPS/PPS 且统一 4 字节起始码。
func normalizeH264Frame(r *h264Ring, key bool, data []byte) (bool, []byte) {
	nalus := splitAnnexBNALUs(data)
	if len(nalus) == 0 {
		return key, data
	}
	var hasSPS, hasPPS, hasIDR bool
	for _, n := range nalus {
		switch n[0] & 0x1F {
		case 7:
			hasSPS = true
			r.sps = append([]byte(nil), n...)
		case 8:
			hasPPS = true
			r.pps = append([]byte(nil), n...)
		case 5:
			hasIDR = true
		}
	}
	if hasIDR {
		key = true
	}
	out := make([]byte, 0, len(data)+len(r.sps)+len(r.pps)+16)
	appendNAL := func(n []byte) {
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	if key {
		if !hasSPS && len(r.sps) > 0 {
			appendNAL(r.sps)
		}
		if !hasPPS && len(r.pps) > 0 {
			appendNAL(r.pps)
		}
	}
	for _, n := range nalus {
		if n[0]&0x1F == 9 { // 跳过 AUD，避免部分 1078 播放器解析异常
			continue
		}
		appendNAL(n)
	}
	if len(out) == 0 {
		return key, data
	}
	return key, out
}

func (t *Terminal) setH264(ch int, key bool, data []byte) error {
	if !validCh(ch) {
		return fmt.Errorf("通道号无效")
	}
	if len(data) == 0 {
		return fmt.Errorf("H.264 帧为空")
	}
	t.mu.Lock()
	if t.h264 == nil {
		t.h264 = map[string]*h264Ring{}
	}
	k := chKey(ch)
	r := t.h264[k]
	if r == nil {
		r = &h264Ring{}
		t.h264[k] = r
	}
	key, norm := normalizeH264Frame(r, key, data)
	r.seq++
	r.items = append(r.items, h264Buf{data: norm, key: key, seq: r.seq, at: time.Now()})
	if len(r.items) > 45 {
		r.items = r.items[len(r.items)-45:]
	}
	t.mu.Unlock()
	return nil
}

// liveH264Next 获取 1078 实时 H.264 帧：优先通道专属，否则回退「全部通道(0)」。
// 返回 (帧, 是否有新帧, 实时流是否活跃)。活跃期内暂无新帧时由调用方跳过本拍，避免回退彩条闪烁。
func (t *Terminal) liveH264Next(ch int, lastSeq uint64, needKey bool) (h264Buf, bool, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	const liveWindow = 2500 * time.Millisecond
	pick := func(k string) *h264Ring {
		r := t.h264[k]
		if r == nil || len(r.items) == 0 {
			return nil
		}
		if time.Since(r.items[len(r.items)-1].at) > liveWindow {
			return nil
		}
		return r
	}
	r := pick(chKey(ch))
	if r == nil && ch != 0 {
		r = pick(chKey(0))
	}
	if r == nil {
		return h264Buf{}, false, false
	}
	if needKey {
		for i := len(r.items) - 1; i >= 0; i-- {
			if r.items[i].key {
				return r.items[i], true, true
			}
		}
		return h264Buf{}, false, true
	}
	for _, it := range r.items {
		if it.seq > lastSeq {
			return it, true, true
		}
	}
	return h264Buf{}, false, true
}

// setFrame 前端回传的当前画面（JPEG），同时进该通道的帧历史（附件多张图片取不同时刻）
func (t *Terminal) setFrame(ch int, data []byte) error {
	if !validCh(ch) {
		return fmt.Errorf("通道号只能是 0（全部）或 64/65/66/67")
	}
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return fmt.Errorf("不是 JPEG 数据")
	}
	t.mu.Lock()
	if t.frames == nil {
		t.frames = map[string][]frameBuf{}
	}
	k := chKey(ch)
	list := make([]frameBuf, 0, maxFrames)
	list = append(list, frameBuf{data, time.Now()})
	list = append(list, t.frames[k]...)
	if len(list) > maxFrames {
		list = list[:maxFrames]
	}
	t.frames[k] = list
	t.mu.Unlock()
	return nil
}

// setClip 记录一段前端录制的视频（MediaRecorder 每约 6 秒一段，只留最近一段）
func (t *Terminal) setClip(ch int, mime string, data []byte) error {
	if !validCh(ch) {
		return fmt.Errorf("通道号只能是 0（全部）或 64/65/66/67")
	}
	if len(data) < 64 {
		return fmt.Errorf("视频片段为空")
	}
	if len(data) > maxClipBytes {
		return fmt.Errorf("视频片段过大（最大 %d MB）", maxClipBytes>>20)
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if !strings.Contains(mime, "mp4") && !strings.Contains(mime, "webm") {
		return fmt.Errorf("只接受 MP4/WebM 视频片段")
	}
	t.mu.Lock()
	if t.clips == nil {
		t.clips = map[string]clipBuf{}
	}
	prev := t.clips[chKey(ch)]
	t.clips[chKey(ch)] = clipBuf{data, mime, time.Now()}
	t.mu.Unlock()
	if prev.mime != mime {
		t.logf("通道 %s 收到录制视频（%s，%d KB），附件视频将使用它", chLabel(ch), mime, len(data)>>10)
	}
	return nil
}

// imageAt 该通道约 back 之前的画面与拍摄时间：视频/摄像头取帧历史里最近的时刻
// （不按时间作废——窗口最小化会让浏览器节流抓帧，按时间作废会莫名其妙退回合成图），
// 图片模式取上传文件（时间按 now-back 模拟连续抓拍）；都没有返回 nil（调用方回退合成图）
func (t *Terminal) imageAt(ch int, back time.Duration) ([]byte, time.Time) {
	t.mu.Lock()
	src, id, fb := t.mediaSrcOf(ch), t.ID, t.frameAgo(ch, back)
	t.mu.Unlock()
	if len(fb.data) > 0 && (src == "video" || src == "camera" || src == "image") {
		return fb.data, fb.at
	}
	if src == "image" {
		if b := readMediaFile(id, ch, ".jpg"); len(b) > 0 {
			return b, time.Now().Add(-back)
		}
	}
	return nil, time.Now().Add(-back)
}

// currentImage 该通道当前画面（预览用）
func (t *Terminal) currentImage(ch int) []byte {
	img, _ := t.imageAt(ch, 0)
	return img
}

// stampEnabled 是否给附件画面烧入时间水印（默认烧，Config.NoStamp 关闭）
func (t *Terminal) stampEnabled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.Cfg.NoStamp
}

// stampIfAt 按需烧入时间水印与速度/经纬度 OSD，水印时间用画面的拍摄时刻（多张图片因此时间递减）
func (t *Terminal) stampIfAt(img []byte, ch int, at time.Time) []byte {
	if len(img) == 0 || !t.stampEnabled() {
		return img
	}
	t.mu.Lock()
	osd := fmt.Sprintf("%.0fKM/H %.4f,%.4f", t.speed, t.Lat, t.Lng)
	t.mu.Unlock()
	return stampJPEGWithOSD(img, ch, at, osd)
}

// stampIf 烧入当前时间的水印
func (t *Terminal) stampIf(img []byte, ch int) []byte { return t.stampIfAt(img, ch, time.Now()) }

// attachmentImageAt 该通道约 back 之前的附件图片（含时间水印）；没有画面返回 nil
func (t *Terminal) attachmentImageAt(ch int, back time.Duration) []byte {
	img, at := t.imageAt(ch, back)
	return t.stampIfAt(img, ch, at)
}

// attachmentImage 该通道当前的附件图片（含时间水印）；没有画面返回 nil（调用方回退合成图）
func (t *Terminal) attachmentImage(ch int) []byte { return t.attachmentImageAt(ch, 0) }

// videoExt 录制片段的后缀：MP4 用协议惯例 .mp4；WebM 如实标 .webm
// （Chrome/WebView2 不一定支持 MediaRecorder 录 MP4，硬改成 .mp4 只会让平台解析失败）
func videoExt(mime string) string {
	if strings.Contains(mime, "webm") {
		return ".webm"
	}
	return ".mp4"
}

// attachmentVideo 该通道视频附件内容与后缀：优先最近一段录制（当前播放画面），
// 其次上传的源视频，最后内置 alarm.mp4（3 秒彩条测试片）
func (t *Terminal) attachmentVideo(ch int) ([]byte, string) {
	data, ext, _ := t.attachmentVideoDetail(ch)
	return data, ext
}

// attachmentVideoDetail 同上，另返回来源（clip 录制 / source 上传的源视频 / builtin 内置彩条片），页面预览用
func (t *Terminal) attachmentVideoDetail(ch int) ([]byte, string, string) {
	t.mu.Lock()
	src, id, clip := t.mediaSrcOf(ch), t.ID, t.clipOf(ch)
	t.mu.Unlock()
	if src == "video" || src == "camera" {
		if len(clip.data) > 0 {
			if age := time.Since(clip.at); age > 30*time.Second {
				t.logf("[附件] 通道 %d 的录制片段已 %d 秒没更新（页面在后台 / 摄像头被关 / 录制失败），仍用这一段 %d KB",
					ch, int(age.Seconds()), len(clip.data)>>10)
			}
			if strings.Contains(clip.mime, "mp4") {
				return clip.data, ".mp4", "clip"
			}
			if src == "camera" { // 摄像头没有源文件可退，只能回传录制的 WebM
				return clip.data, videoExt(clip.mime), "clip"
			}
		}
	}
	if src == "video" {
		if b := readMediaFile(id, ch, ".mp4"); b != nil {
			return b, ".mp4", "source"
		}
	}
	if src == "video" || src == "camera" {
		// 附件视频回退到内置彩条片时明确说明原因，便于区分「录制没起来」和「媒体源没设」
		t.logf("[附件] 通道 %d 无可用录制片段（来源=%s，片段=%d 字节），视频附件用内置 alarm.mp4", ch, src, len(clip.data))
	}
	return alarmMP4, ".mp4", "builtin"
}

// mediaView 各通道媒体源状态（页面显示用）。调用方持有 t.mu（State 内调用）
func (t *Terminal) mediaView() map[string]any {
	out := make(map[string]any, len(mediaChs)+1)
	for _, ch := range append([]int{0}, mediaChs...) {
		src, fb, clip := t.mediaSrcOf(ch), t.frameOf(ch), t.clipOf(ch)
		age, clipAge := -1, -1
		if !fb.at.IsZero() {
			age = int(time.Since(fb.at).Seconds())
		}
		if !clip.at.IsZero() {
			clipAge = int(time.Since(clip.at).Seconds())
		}
		own := src != ""
		if ch != 0 {
			_, own = t.Cfg.MediaCh[chKey(ch)] // 该通道是否独立设置（否则继承全部通道）
		}
		out[chKey(ch)] = map[string]any{
			"src": src, "own": own,
			"frameAge": age, "frameSize": len(fb.data),
			"stale":   age > int(frameStale.Seconds()), // 前端抓帧可能被浏览器节流，仅用于页面提示
			"clipAge": clipAge, "clipSize": len(clip.data), "clipMime": clip.mime,
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
		var path, ctype string
		if src == "image" {
			targetCh := ch
			if _, err := os.Stat(mediaFile(id, ch, ".jpg")); err != nil {
				if _, err := os.Stat(mediaFile(id, ch, ".gif")); err != nil {
					targetCh = 0 // 通道继承全局设置时用全局文件
				}
			}
			if gp := mediaFile(id, targetCh, ".gif"); func() bool { _, err := os.Stat(gp); return err == nil }() {
				path, ctype = gp, "image/gif"
			} else {
				path, ctype = mediaFile(id, targetCh, ".jpg"), "image/jpeg"
			}
		} else {
			path, ctype = mediaFile(id, ch, ".mp4"), "video/mp4"
			if _, err := os.Stat(path); err != nil {
				path = mediaFile(id, 0, ".mp4") // 通道继承全局设置时用全局文件
			}
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
		w.Header().Set("Content-Type", ctype)
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
	handle("POST /h264", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		ch, ok := queryCh(r)
		if !ok {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "通道号无效"})
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFrameBytes))
		if err != nil || t.setH264(ch, r.URL.Query().Get("key") == "1", data) != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "无效 H.264 帧"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	// 录制片段：前端 MediaRecorder 每约 6 秒把一段视频回传，附件视频用最近一段（POST videoclip）
	handle("POST /videoclip", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		ch, ok := queryCh(r)
		if !ok {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "通道号无效"})
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxClipBytes))
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "读取失败: " + err.Error()})
			return
		}
		if err := t.setClip(ch, r.Header.Get("Content-Type"), data); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	// 预览：该通道此刻会用于附件的视频（响应头 X-JT808-Source 标注是录制片段 / 源视频 / 内置彩条片）
	handle("GET /videoclip", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		ch, ok := queryCh(r)
		if !ok {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "通道号只能是 0（全部）或 64/65/66/67"})
			return
		}
		data, ext, source := t.attachmentVideoDetail(ch)
		ct := "video/mp4"
		if ext == ".webm" {
			ct = "video/webm"
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-JT808-Source", source)
		w.Header().Set("X-JT808-Channel", strconv.Itoa(ch))
		w.Write(data)
	})
	handle("POST /mediasrc/stamp", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		on := r.URL.Query().Get("on") != "0"
		t.mu.Lock()
		t.Cfg.NoStamp = !on
		t.markDirty()
		t.mu.Unlock()
		t.logf("附件画面时间水印已%s", map[bool]string{true: "开启", false: "关闭"}[on])
		writeJSON(w, 200, map[string]any{"ok": true, "on": on})
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
		var img []byte
		source := "image"
		switch {
		case len(fb.data) > 0 && (srcTyp == "video" || srcTyp == "camera"):
			img, source = fb.data, "frame"
		case srcTyp == "image":
			img = t.currentImage(ch)
		}
		if img == nil {
			jc := ch
			if jc == 0 {
				jc = 64
			}
			img, source = makeJPEG(jc, 2, 3, time.Now()), "synthetic"
		}
		img = t.stampIf(img, ch)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("X-JT808-Source", source)
		w.Header().Set("X-JT808-Channel", strconv.Itoa(ch))
		w.Write(img)
	})
}
