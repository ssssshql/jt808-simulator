package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// 内嵌静态资源：index 不缓存且引用带版本号；css/js 类型正确；带当前版本号的资源长期缓存
func TestWebStatic(t *testing.T) {
	h, ver := webHandler()
	get := func(path string) (*httptest.ResponseRecorder, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		b, _ := io.ReadAll(w.Result().Body)
		return w, string(b)
	}
	w, body := get("/")
	for _, js := range []string{"scenario.js", "fleet.js"} {
		if !strings.Contains(body, `src="`+js+`?v=`) {
			t.Errorf("index 未引用 %s", js)
		}
	}
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(body, `href="style.css?v=`+ver+`"`) ||
		!strings.Contains(body, `src="pages.js?v=`+ver+`"`) || !strings.Contains(body, `src="vendor/leaflet/leaflet.js?v=`+ver+`"`) || !strings.Contains(body, `content="`+ver+`"`) {
		t.Fatalf("index: %d %v", w.Code, w.Header())
	}
	for path, ct := range map[string]string{"/style.css": "text/css", "/app.js": "javascript", "/pages.js": "javascript", "/params.js": "javascript",
		"/log.js": "javascript", "/map.js": "javascript", "/scenario.js": "javascript", "/fleet.js": "javascript", "/stress.js": "javascript", "/vendor/leaflet/leaflet.css": "text/css", "/vendor/leaflet/leaflet.js": "javascript", "/vendor/leaflet/images/layers.png": "image/png"} {
		w, b := get(path + "?v=" + ver)
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), ct) || len(b) == 0 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("%s: %d %q %q", path, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
		}
	}
	if w, _ := get("/style.css"); w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("无版本号的资源应 no-cache: %q", w.Header().Get("Cache-Control"))
	}
}
