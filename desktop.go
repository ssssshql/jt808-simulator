//go:build desktop

package main

// 桌面版（Wails）：不带参数启动时，在 127.0.0.1 随机端口起内置 Web 服务，用 WebView2 窗口打开。
// 后端与 -web 模式完全相同（含 SSE 日志），只是不再需要浏览器和服务器。
// 窗口无边框，标题栏与窗口按钮在页面里（web/desktop.js）。
// 状态保存在 %APPDATA%\jt808sim\state；带任意参数启动仍是原来的命令行 / -web 模式。
// 构建：go build -tags desktop,production -ldflags "-H windowsgui" -o jt808sim.exe .

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func init() { desktopMain = runDesktop }

func desktopStateDir() string {
	base, err := os.UserConfigDir() // Windows: %APPDATA%
	if err != nil {
		exe, _ := os.Executable()
		return filepath.Join(filepath.Dir(exe), "state")
	}
	return filepath.Join(base, "jt808sim", "state")
}

func fatalBox(msg string) {
	// GUI 程序没有控制台，错误写到状态目录旁的日志
	dir := filepath.Dir(desktopStateDir())
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "error.log"), []byte(msg+"\n"), 0o644)
	os.Exit(1)
}

func runDesktop() {
	stateDir = desktopStateDir()
	os.MkdirAll(stateDir, 0o755)

	m := NewManager(Config{})
	logDisk.Start() // 报文日志落盘（state/logs，可在页面关闭）
	go m.saveLoop()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatalBox("监听本地端口失败: " + err.Error())
	}
	url := "http://" + ln.Addr().String() + "/"
	logf("%s  桌面版，内置服务: %s  状态目录: %s", versionString(), url, stateDir)
	go http.Serve(ln, newWebMux(m))

	// 启动页只做一次跳转：窗口直接加载本地服务，SSE / 长连接不经过 Wails 资源服务器
	boot := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><title>JT808 模拟器</title>
<body style="font-family:sans-serif;color:#888;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">正在启动…
<script>location.replace(%q)</script></body>`, url)

	// 记住窗口：上次的位置 / 大小 / 是否最大化（window.json 在状态目录内）
	winFile := filepath.Join(stateDir, "window.json")
	saved := loadWindowState(winFile)
	startState := options.Normal
	if saved != nil && saved.Maximised {
		startState = options.Maximised
	}
	var winOnce sync.Once
	saveWin := func(ctx context.Context) {
		winOnce.Do(func() {
			if ws, ok := captureWindow(ctx); ok {
				if err := saveWindowState(winFile, ws); err != nil {
					errf("保存窗口状态失败: %v", err)
				}
			}
		})
	}

	err = wails.Run(&options.App{
		Title:     "JT808 模拟器 " + buildInfo().Version,
		Width:     1440,
		Height:    900,
		MinWidth:  1200,
		MinHeight: 720,
		// 最大化在首次显示时由 Wails 处理（显示在还原矩形所在的显示器上）
		WindowStartState: startState,
		// 无边框：标题栏、窗口按钮、拖动与缩放由页面自绘（web/desktop.js 通过 WebView2 postMessage 交给 Wails）
		Frameless:        true,
		BackgroundColour: &options.RGBA{R: 241, G: 240, B: 236, A: 255},
		// 页面来自本地服务 http://127.0.0.1:<端口>，需允许它向 Wails 发送窗口消息（只放行这一个精确来源）
		BindingsAllowedOrigins: strings.TrimSuffix(url, "/"),
		AssetServer: &assetserver.Options{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write([]byte(boot))
			}),
		},
		SingleInstanceLock: &options.SingleInstanceLock{UniqueId: "jt808sim-desktop-5f1c2e"},
		OnStartup: func(ctx context.Context) {
			// 窗口已创建但尚未显示：先放到上次的位置（越界则居中）
			if saved != nil {
				if r, centered, ok := applyWindow(ctx, *saved, 1200, 720); ok {
					logf("恢复窗口 %d,%d %dx%d%s%s", r.L, r.T, r.w(), r.h(), map[bool]string{true: "（上次位置不可见，已居中）"}[centered],
						map[bool]string{true: " 最大化"}[saved.Maximised])
				}
			}
			updateQuit = func() { saveWin(ctx); m.SaveAll(); wruntime.Quit(ctx) }
			saveFileHook = func(name string, data []byte) (string, error) { return desktopSave(ctx, name, data) }
		},
		// 正常关闭窗口（页面关闭按钮 'Q' / Alt+F4 / 任务栏关闭）都经过这里：
		// 窗口销毁前保存位置与大小；否则只有程序内更新退出才会写 window.json，
		// “记住窗口”在日常使用中永远不生效
		OnBeforeClose: func(ctx context.Context) bool {
			saveWin(ctx)
			return false // 不阻止关闭
		},
		OnShutdown: func(ctx context.Context) { m.SaveAll() },
		Windows: &windows.Options{
			WebviewUserDataPath: filepath.Join(filepath.Dir(stateDir), "webview"),
			WindowClassName:     winClassName,
		},
	})
	m.SaveAll()
	if err != nil {
		fatalBox("窗口启动失败: " + err.Error())
	}
}

// desktopSave 系统“另存为”对话框（导出日志 / 报告 / 场景等）
func desktopSave(ctx context.Context, name string, data []byte) (string, error) {
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	opts := wruntime.SaveDialogOptions{DefaultFilename: name, Title: "保存文件", CanCreateDirectories: true}
	if ext != "" {
		opts.Filters = []wruntime.FileFilter{{DisplayName: strings.ToUpper(ext) + " 文件 (*." + ext + ")", Pattern: "*." + ext}, {DisplayName: "所有文件 (*.*)", Pattern: "*.*"}}
	}
	if dir, err := os.UserHomeDir(); err == nil {
		if d := filepath.Join(dir, "Downloads"); dirExists(d) {
			opts.DefaultDirectory = d
		}
	}
	path, err := wruntime.SaveFileDialog(ctx, opts)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", errSaveCancelled
	}
	if ext != "" && filepath.Ext(path) == "" {
		path += "." + ext
	}
	return path, os.WriteFile(path, data, 0o644)
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
