package main

import (
	"path/filepath"
	"testing"
)

func TestWindowStateFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "window.json")
	if loadWindowState(p) != nil {
		t.Fatal("不存在时应为 nil")
	}
	if err := saveWindowState(p, WindowState{X: -1800, Y: 40, W: 1500, H: 900, Maximised: true}); err != nil {
		t.Fatal(err)
	}
	ws := loadWindowState(p)
	if ws == nil || ws.X != -1800 || ws.W != 1500 || !ws.Maximised || ws.Saved == "" {
		t.Fatalf("%+v", ws)
	}
	saveWindowState(p, WindowState{}) // 无效尺寸不覆盖
	if loadWindowState(p).W != 1500 {
		t.Fatal("被无效状态覆盖")
	}
}

func TestPlaceWindow(t *testing.T) {
	primary := winRect{0, 0, 1920, 1040}  // 1080p，任务栏 40
	left := winRect{-2560, -200, 0, 1240} // 左侧 2K 副屏
	small := winRect{0, 0, 1366, 728}     // 换成小屏
	cases := []struct {
		name     string
		ws       WindowState
		works    []winRect
		want     winRect
		centered bool
	}{
		{"主屏内原样恢复", WindowState{X: 100, Y: 50, W: 1440, H: 900}, []winRect{primary}, winRect{100, 50, 1540, 950}, false},
		{"副屏内原样恢复", WindowState{X: -2400, Y: -100, W: 1600, H: 1000}, []winRect{primary, left}, winRect{-2400, -100, -800, 900}, false},
		{"副屏拔掉后居中", WindowState{X: -2400, Y: -100, W: 1600, H: 1000}, []winRect{primary}, winRect{160, 20, 1760, 1020}, true},
		{"完全在屏幕外居中", WindowState{X: 5000, Y: 5000, W: 1440, H: 900}, []winRect{primary}, winRect{240, 70, 1680, 970}, true},
		{"标题栏在屏幕上方外居中", WindowState{X: 100, Y: -500, W: 1440, H: 900}, []winRect{primary}, winRect{240, 70, 1680, 970}, true},
		{"小屏：尺寸收缩并收回", WindowState{X: 200, Y: 100, W: 1440, H: 900}, []winRect{small}, winRect{0, 0, 1366, 728}, false},
		{"小于最小尺寸时放大", WindowState{X: 10, Y: 10, W: 300, H: 200}, []winRect{primary}, winRect{10, 10, 1210, 730}, false},
		{"右侧露出一点也算可见并收回", WindowState{X: 1700, Y: 100, W: 1440, H: 900}, []winRect{primary}, winRect{480, 100, 1920, 1000}, false},
	}
	for _, c := range cases {
		got, cen := placeWindow(c.ws, c.works, c.works[0], 1200, 720)
		if c.name == "副屏拔掉后居中" || c.name == "完全在屏幕外居中" || c.name == "标题栏在屏幕上方外居中" {
			got, cen = placeWindow(c.ws, c.works, primary, 1200, 720)
		}
		if got != c.want || cen != c.centered {
			t.Errorf("%s: got %+v centered=%v, want %+v %v", c.name, got, cen, c.want, c.centered)
		}
	}
}
