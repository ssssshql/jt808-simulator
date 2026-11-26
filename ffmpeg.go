package main

// 可选本机 ffmpeg 1078 实时 H.264 推流：
// 仅在系统 PATH 存在 ffmpeg 时启用（不捆绑任何 DLL，保持单 exe 零依赖）。
// 输出 Annex-B H.264 裸流并按帧写入 t.putH264(0, ...)，自动接入正在运行的 9101 推流。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

type ffmpegState struct {
	mu      sync.Mutex
	running bool
	source  string
	cmd     *exec.Cmd
	frames  int64
	lastErr string
}

var ffState = map[string]*ffmpegState{}
var ffMu sync.Mutex

func getFFState(id string) *ffmpegState {
	ffMu.Lock()
	defer ffMu.Unlock()
	s := ffState[id]
	if s == nil {
		s = &ffmpegState{}
		ffState[id] = s
	}
	return s
}

func hasFFmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func registerFFmpegAPI(handle func(string, termHandler)) {
	handle("GET /ffmpeg", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		s := getFFState(t.ID)
		s.mu.Lock()
		res := map[string]any{
			"available": hasFFmpeg(),
			"running":   s.running,
			"source":    s.source,
			"frames":    s.frames,
			"error":     s.lastErr,
		}
		s.mu.Unlock()
		writeJSON(w, 200, res)
	})
	handle("POST /ffmpeg", func(w http.ResponseWriter, r *http.Request, t *Terminal) {
		var p map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "JSON 无效"})
			return
		}
		op := pstr(p, "op", "start")
		s := getFFState(t.ID)
		if op == "stop" {
			stopFFmpeg(s)
			writeJSON(w, 200, map[string]any{"ok": true})
			return
		}
		if !hasFFmpeg() {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "本机未检测到 ffmpeg（加入 PATH 后即可使用）"})
			return
		}
		src := strings.TrimSpace(pstr(p, "source", "desktop"))
		if err := startFFmpeg(t, s, src); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
}

func stopFFmpeg(s *ffmpegState) {
	s.mu.Lock()
	cmd := s.cmd
	s.cmd = nil
	s.running = false
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func startFFmpeg(t *Terminal, s *ffmpegState, src string) error {
	stopFFmpeg(s)
	var inArgs []string
	switch {
	case src == "desktop" && runtime.GOOS == "windows":
		inArgs = []string{"-f", "gdigrab", "-framerate", "15", "-i", "desktop"}
	case strings.HasPrefix(src, "dshow:") && runtime.GOOS == "windows":
		dev := strings.TrimPrefix(src, "dshow:")
		inArgs = []string{"-f", "dshow", "-i", "video=" + dev}
	default:
		inArgs = []string{"-f", "lavfi", "-re", "-i", "testsrc=size=352x288:rate=15"}
	}
	args := append(inArgs, "-vf", "scale=352:288", "-r", "15", "-c:v", "libx264", "-preset", "ultrafast",
		"-tune", "zerolatency", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-g", "15", "-f", "h264", "pipe:1")
	cmd := exec.Command("ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 ffmpeg 失败: %v", err)
	}
	s.mu.Lock()
	s.cmd = cmd
	s.running = true
	s.source = src
	s.frames = 0
	s.lastErr = ""
	s.mu.Unlock()
	t.logf("ffmpeg 本地推流已启动: %s", src)
	go func() {
		defer func() {
			_ = cmd.Wait()
			s.mu.Lock()
			if s.cmd == cmd {
				s.running = false
				s.cmd = nil
			}
			s.mu.Unlock()
		}()
		readAnnexBStream(stdout, func(au []byte, key bool) {
			if err := t.setH264(0, key, au); err == nil {
				s.mu.Lock()
				s.frames++
				s.mu.Unlock()
			}
		})
	}()
	return nil
}

// readAnnexBStream 从 ffmpeg h264 裸流切分单个 NALU/帧并回调
func readAnnexBStream(r io.Reader, onNAL func(au []byte, key bool)) {
	sc4 := []byte{0, 0, 0, 1}
	tmp := make([]byte, 32768)
	var buf []byte
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				i := bytes.Index(buf, sc4)
				if i < 0 {
					break
				}
				j := bytes.Index(buf[i+4:], sc4)
				if j < 0 {
					if i > 0 {
						buf = buf[i:]
					}
					break
				}
				nal := buf[i : i+4+j]
				nt := nal[4] & 0x1f
				onNAL(append([]byte(nil), nal...), nt == 5 || nt == 7)
				buf = buf[i+4+j:]
			}
			if len(buf) > 512<<10 {
				buf = buf[:0]
			}
		}
		if err != nil {
			return
		}
	}
}
