package main

// Web 静态资源：内嵌 web/，按内容哈希做缓存版本。
// index.html 不缓存（no-cache），其中的 css/js 引用自动加 ?v=<版本>；带当前版本号的资源长期缓存，
// 其它请求一律 no-cache + ETag 校验。这样经过 Cloudflare 等 CDN 或浏览器缓存时，新旧页面与脚本不会混用。

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

var assetRef = regexp.MustCompile(`((?:href|src)=")([^":?#]+\.(?:css|js))(")`)

// webVersion 返回 web/ 下全部文件内容的哈希前 10 位
func webVersion(sub fs.FS) string {
	h := sha256.New()
	var names []string
	fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	for _, n := range names {
		b, _ := fs.ReadFile(sub, n)
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:10]
}

// versionIndex 给 index.html 中的本地 css/js 引用加版本号，并写入 <meta name="jt808-build">
func versionIndex(html, ver string) string {
	html = assetRef.ReplaceAllString(html, "${1}${2}?v="+ver+"${3}")
	return strings.Replace(html, "<head>", `<head>
<meta name="jt808-build" content="`+ver+`">`, 1)
}

func webHandler() (http.Handler, string) {
	sub, _ := fs.Sub(webFS, "web")
	ver := webVersion(sub)
	raw, _ := fs.ReadFile(sub, "index.html")
	index := []byte(versionIndex(string(raw), ver))
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-JT808-Build", ver)
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(index)
			return
		}
		if r.URL.Query().Get("v") == ver {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", `"`+ver+`"`)
		}
		files.ServeHTTP(w, r)
	}), ver
}
