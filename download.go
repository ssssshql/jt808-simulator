package main

// 文件下载：浏览器里直接返回附件（Content-Disposition）；桌面版（WebView2）由后端弹出系统“另存为”对话框写文件，
// 不依赖 WebView2 对 a[download] / blob 下载的支持。

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// saveFileHook 桌面版设置：弹出“另存为”对话框并写入 data，返回保存路径；用户取消返回 errSaveCancelled
var saveFileHook func(name string, data []byte) (string, error)

var errSaveCancelled = fmt.Errorf("已取消保存")

const maxSaveBytes = 64 << 20

// safeName 去掉路径与非法字符的文件名
func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		name = "download"
	}
	return name
}

// serveDownload ?save=1 且为桌面版时弹出保存对话框（返回 JSON）；否则作为附件下载
func serveDownload(w http.ResponseWriter, r *http.Request, name, ctype string, data []byte) {
	name = safeName(name)
	if r.URL.Query().Get("save") == "1" {
		saveViaHook(w, name, data)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

func saveViaHook(w http.ResponseWriter, name string, data []byte) {
	if saveFileHook == nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "只有桌面版支持保存对话框"})
		return
	}
	path, err := saveFileHook(name, data)
	if err == errSaveCancelled {
		writeJSON(w, 200, map[string]any{"ok": false, "cancelled": true})
		return
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "保存失败: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": path})
}

// registerDownloadAPI POST /api/savefile?name=xxx（请求体为文件内容）：页面生成的文件在桌面版中经保存对话框写盘
func registerDownloadAPI(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/savefile", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSaveBytes))
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "读取失败: " + err.Error()})
			return
		}
		saveViaHook(w, safeName(r.URL.Query().Get("name")), data)
	})
}
