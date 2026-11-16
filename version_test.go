package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersionInfo(t *testing.T) {
	old := version
	defer func() { version = old }()
	version, commit, buildTime = "v9.9.9", "abcdef12", "2026-01-01T00:00:00Z"
	defer func() { commit, buildTime = "", "" }()
	v := buildInfo()
	if v.Version != "v9.9.9" || v.Commit != "abcdef12" || v.BuildTime != "2026-01-01T00:00:00Z" || v.Go == "" {
		t.Fatalf("%+v", v)
	}
	if s := versionString(); !strings.Contains(s, "v9.9.9") || !strings.Contains(s, "abcdef12") {
		t.Fatal(s)
	}
	h := withVersion(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(buildInfo()) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/version", nil))
	if w.Header().Get("X-JT808-Version") != "v9.9.9" {
		t.Fatal(w.Header())
	}
}
