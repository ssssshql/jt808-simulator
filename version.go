package main

// 版本信息：构建时通过 -ldflags 注入，例：
//   go build -ldflags "-X main.version=$(git describe --tags --always --dirty) -X main.commit=$(git rev-parse --short HEAD) -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
// 未注入时（go run / go build）回退到 Go 内置的 VCS 信息。

import (
	"runtime"
	"runtime/debug"
)

var (
	version   = "dev"
	commit    = ""
	buildTime = ""
)

type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
	Go        string `json:"go"`
	Platform  string `json:"platform"`
	Web       string `json:"web,omitempty"` // 前端静态资源哈希（X-JT808-Build）
}

func buildInfo() VersionInfo {
	v := VersionInfo{Version: version, Commit: commit, BuildTime: buildTime, Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		dirty := false
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if v.Commit == "" && len(s.Value) >= 8 {
					v.Commit = s.Value[:8]
				}
			case "vcs.time":
				if v.BuildTime == "" {
					v.BuildTime = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if v.Version == "dev" && v.Commit != "" {
			v.Version = "dev-" + v.Commit
			if dirty {
				v.Version += "-dirty"
			}
		}
	}
	return v
}

// versionString 一行版本说明（-version 与启动日志）
func versionString() string {
	v := buildInfo()
	s := "jt808sim " + v.Version
	if v.Commit != "" && v.Version != "dev-"+v.Commit {
		s += " (" + v.Commit + ")"
	}
	if v.BuildTime != "" {
		s += " built " + v.BuildTime
	}
	return s + " " + v.Go + " " + v.Platform
}
