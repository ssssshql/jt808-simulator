package main

// 检查更新：读取 GitHub 公开仓库的最新 Release，与当前版本比较。
// 安装版（exe 旁有 uninstall.exe，Windows）可在程序内下载安装包、校验 SHA256 后启动安装并退出；
// 便携版和命令行版只提示新版本并打开发布页。

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const updateRepo = "ssssshql/jt808-simulator"

var (
	updateAPI  = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
	updatePage = "https://github.com/" + updateRepo + "/releases/latest"
)

type UpdateInfo struct {
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	Newer      bool   `json:"newer"`
	URL        string `json:"url"`
	Notes      string `json:"notes"`
	Published  string `json:"published"`
	SetupURL   string `json:"-"`
	SetupName  string `json:"setupName"`
	SetupSize  int64  `json:"setupSize"`
	SumsURL    string `json:"-"`
	CanInstall bool   `json:"canInstall"`
	Checked    string `json:"checked"`
}

type updateProgress struct {
	State string `json:"state"` // idle / downloading / verifying / launching / error
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
}

var upd struct {
	sync.Mutex
	info *UpdateInfo
	at   time.Time
	prog updateProgress
}

var updateClient = &http.Client{Timeout: 20 * time.Second}

// parseVer 解析 v1.2.3 / 1.2.3-rc1，返回主次补丁号；dev 或无法解析时 ok=false
func parseVer(s string) (v [3]int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "-")
	s, _, _ = strings.Cut(s, "+")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// newerThan latest 是否比 cur 新；cur 为 dev 等非正式版本时不提示更新
func newerThan(latest, cur string) bool {
	l, ok1 := parseVer(latest)
	c, ok2 := parseVer(cur)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// installedCopy 是否为安装包安装的副本（exe 同目录有 uninstall.exe）
func installedCopy() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(filepath.Dir(exe), "uninstall.exe"))
	return err == nil
}

func fetchLatest(ctx context.Context) (*UpdateInfo, error) {
	cur := buildInfo().Version
	info := &UpdateInfo{Current: cur, URL: updatePage, Checked: time.Now().Format(time.RFC3339)}
	req, _ := http.NewRequestWithContext(ctx, "GET", updateAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "jt808sim/"+cur)
	resp, err := updateClient.Do(req)
	if err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()
		var rel struct {
			Tag       string `json:"tag_name"`
			HTMLURL   string `json:"html_url"`
			Body      string `json:"body"`
			Published string `json:"published_at"`
			Assets    []struct {
				Name string `json:"name"`
				Size int64  `json:"size"`
				URL  string `json:"browser_download_url"`
			} `json:"assets"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
			return nil, fmt.Errorf("解析发布信息失败: %w", err)
		}
		info.Latest, info.Notes, info.Published = rel.Tag, rel.Body, rel.Published
		if rel.HTMLURL != "" {
			info.URL = rel.HTMLURL
		}
		for _, a := range rel.Assets {
			switch {
			case strings.HasSuffix(a.Name, "-windows-x64-setup.exe"):
				info.SetupURL, info.SetupName, info.SetupSize = a.URL, a.Name, a.Size
			case a.Name == "SHA256SUMS":
				info.SumsURL = a.URL
			}
		}
	} else {
		// API 受限（匿名每小时 60 次）或不可用：改用发布页跳转取最新标签，资源按命名规则拼出
		if resp != nil {
			resp.Body.Close()
		}
		tag, err2 := latestTagByRedirect(ctx)
		if err2 != nil {
			if err == nil {
				err = fmt.Errorf("GitHub 返回 %s", resp.Status)
			}
			return nil, fmt.Errorf("检查更新失败: %v", err)
		}
		base := "https://github.com/" + updateRepo + "/releases/download/" + tag + "/"
		info.Latest, info.URL = tag, "https://github.com/"+updateRepo+"/releases/tag/"+tag
		info.SetupName = "jt808sim-" + tag + "-windows-x64-setup.exe"
		info.SetupURL, info.SumsURL = base+info.SetupName, base+"SHA256SUMS"
	}
	info.Newer = newerThan(info.Latest, cur)
	info.CanInstall = info.Newer && info.SetupURL != "" && installedCopy()
	return info, nil
}

func latestTagByRedirect(ctx context.Context) (string, error) {
	c := *updateClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequestWithContext(ctx, "HEAD", updatePage, nil)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/")
	if i < 0 {
		return "", errors.New("发布页没有返回版本标签")
	}
	return loc[i+5:], nil
}

// checkUpdate 带 10 分钟缓存；force 时重新获取
func checkUpdate(ctx context.Context, force bool) (*UpdateInfo, error) {
	upd.Lock()
	if !force && upd.info != nil && time.Since(upd.at) < 10*time.Minute {
		i := upd.info
		upd.Unlock()
		return i, nil
	}
	upd.Unlock()
	info, err := fetchLatest(ctx)
	if err != nil {
		return nil, err
	}
	upd.Lock()
	upd.info, upd.at = info, time.Now()
	upd.Unlock()
	return info, nil
}

func setProg(p updateProgress) { upd.Lock(); upd.prog = p; upd.Unlock() }

type progWriter struct{ n int64 }

func (w *progWriter) Write(b []byte) (int, error) {
	w.n += int64(len(b))
	upd.Lock()
	upd.prog.Done = w.n
	upd.Unlock()
	return len(b), nil
}

// installUpdate 后台下载安装包 → 校验 SHA256 → 启动安装程序 → 退出（安装程序会覆盖 exe）
func installUpdate(info *UpdateInfo, quit func()) {
	fail := func(err error) { setProg(updateProgress{State: "error", Error: err.Error()}) }
	setProg(updateProgress{State: "downloading", Total: info.SetupSize})
	dir := filepath.Join(os.TempDir(), "jt808sim-update")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, info.SetupName)

	dl := &http.Client{Timeout: 30 * time.Minute}
	resp, err := dl.Get(info.SetupURL)
	if err != nil {
		fail(fmt.Errorf("下载失败: %w", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fail(fmt.Errorf("下载失败: %s", resp.Status))
		return
	}
	if resp.ContentLength > 0 {
		upd.Lock()
		upd.prog.Total = resp.ContentLength
		upd.Unlock()
	}
	f, err := os.Create(path)
	if err != nil {
		fail(err)
		return
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h, &progWriter{}), resp.Body)
	f.Close()
	if err != nil {
		fail(fmt.Errorf("下载中断: %w", err))
		return
	}

	setProg(updateProgress{State: "verifying"})
	want, err := expectedSum(info.SumsURL, info.SetupName)
	if err != nil {
		fail(err)
		return
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		os.Remove(path)
		fail(errors.New("安装包校验失败（SHA256 不一致），已删除"))
		return
	}

	setProg(updateProgress{State: "launching"})
	if err := exec.Command(path).Start(); err != nil {
		fail(fmt.Errorf("启动安装程序失败: %w", err))
		return
	}
	logf("已启动更新安装程序 %s，程序退出", info.SetupName)
	time.Sleep(800 * time.Millisecond)
	quit()
}

func expectedSum(url, name string) (string, error) {
	if url == "" {
		return "", errors.New("发布中没有 SHA256SUMS，无法校验")
	}
	resp, err := updateClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("获取校验文件失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("获取校验文件失败: %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0], nil
		}
	}
	return "", errors.New("校验文件里没有 " + name)
}

// updateQuit 由桌面版设置为关闭窗口；命令行版直接退出进程
var updateQuit = func() { os.Exit(0) }

func registerUpdateAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		info, err := checkUpdate(ctx, r.URL.Query().Get("force") == "1")
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error(), "current": buildInfo().Version, "url": updatePage})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "info": info})
	})
	mux.HandleFunc("GET /api/update/progress", func(w http.ResponseWriter, r *http.Request) {
		upd.Lock()
		p := upd.prog
		upd.Unlock()
		writeJSON(w, 200, p)
	})
	mux.HandleFunc("POST /api/update/install", func(w http.ResponseWriter, r *http.Request) {
		upd.Lock()
		info, busy := upd.info, upd.prog.State
		upd.Unlock()
		if info == nil || !info.CanInstall {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "当前版本不支持程序内更新，请到发布页下载"})
			return
		}
		if busy == "downloading" || busy == "verifying" || busy == "launching" {
			writeJSON(w, 200, map[string]any{"ok": true})
			return
		}
		go installUpdate(info, updateQuit)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	// 用系统浏览器打开链接（桌面窗口里点外链不会跳出 WebView），只允许本仓库页面
	mux.HandleFunc("POST /api/open", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ URL string }
		json.NewDecoder(r.Body).Decode(&b)
		if !strings.HasPrefix(b.URL, "https://github.com/"+updateRepo) {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "不允许打开该链接"})
			return
		}
		openBrowser(b.URL)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
}
