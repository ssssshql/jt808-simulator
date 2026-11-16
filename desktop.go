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

	err = wails.Run(&options.App{
		Title:     "JT808 模拟器 " + buildInfo().Version,
		Width:     1440,
		Height:    900,
		MinWidth:  1200,
		MinHeight: 720,
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
		OnStartup:          func(ctx context.Context) { updateQuit = func() { m.SaveAll(); wruntime.Quit(ctx) } },
		OnShutdown:         func(ctx context.Context) { m.SaveAll() },
		Windows: &windows.Options{
			WebviewUserDataPath: filepath.Join(filepath.Dir(stateDir), "webview"),
		},
	})
	m.SaveAll()
	if err != nil {
		fatalBox("窗口启动失败: " + err.Error())
	}
}
