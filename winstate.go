package main

// 桌面版记住窗口：退出时把窗口位置、大小、是否最大化写到状态目录旁的 window.json，启动时恢复。
// 这里只有与平台无关的部分（读写、位置校验），便于单元测试；取/设窗口位置在 winstate_windows.go / winstate_other.go。

import (
	"encoding/json"
	"time"
)

// WindowState 窗口状态。X/Y/W/H 为“还原”状态下的窗口矩形（屏幕坐标，物理像素）；最大化时仍记录还原后的矩形
type WindowState struct {
	X         int    `json:"x"`
	Y         int    `json:"y"`
	W         int    `json:"w"`
	H         int    `json:"h"`
	Maximised bool   `json:"maximised"`
	Saved     string `json:"saved,omitempty"`
}

// winRect 屏幕矩形（右、下边界不含）
type winRect struct{ L, T, R, B int }

func (r winRect) w() int { return r.R - r.L }
func (r winRect) h() int { return r.B - r.T }

func intersectW(a, b winRect) (int, int) {
	return max(0, min(a.R, b.R)-max(a.L, b.L)), max(0, min(a.B, b.B)-max(a.T, b.T))
}

func loadWindowState(path string) *WindowState {
	var ws WindowState
	if _, err := readJSONDurable(path, &ws); err != nil || ws.W <= 0 || ws.H <= 0 {
		return nil
	}
	return &ws
}

func saveWindowState(path string, ws WindowState) error {
	if ws.W <= 0 || ws.H <= 0 {
		return nil
	}
	ws.Saved = time.Now().Format(time.RFC3339)
	b, _ := json.MarshalIndent(ws, "", " ")
	return writeFileDurable(path, b)
}

// placeWindow 计算恢复后的窗口矩形：
//   - 尺寸不小于 minW×minH，不大于所在显示器（找不到则主显示器）的工作区；
//   - 标题栏（顶部 32 像素）与任一显示器工作区重叠至少 100×16 像素才算“可见”，
//     否则（拔掉了副屏、分辨率变小、坐标异常）居中到主显示器工作区。
//
// 返回的 centered 表示发生了居中。
func placeWindow(ws WindowState, works []winRect, primary winRect, minW, minH int) (r winRect, centered bool) {
	w, h := max(ws.W, minW), max(ws.H, minH)
	r = winRect{ws.X, ws.Y, ws.X + w, ws.Y + h}
	bar := winRect{r.L, r.T, r.R, r.T + 32}
	var on *winRect
	for i := range works {
		if iw, ih := intersectW(bar, works[i]); iw >= min(100, w) && ih >= 16 {
			on = &works[i]
			break
		}
	}
	if on == nil {
		on, centered = &primary, true
	}
	w, h = min(w, on.w()), min(h, on.h())
	if centered {
		x, y := on.L+(on.w()-w)/2, on.T+(on.h()-h)/2
		return winRect{x, y, x + w, y + h}, true
	}
	// 可见但部分超出：顶部不能高于工作区（否则标题栏拖不到），整体尽量收回工作区
	x, y := r.L, max(r.T, on.T)
	if x+w > on.R {
		x = max(on.L, on.R-w)
	}
	if x < on.L && x+w < on.L+100 {
		x = on.L
	}
	if y+h > on.B {
		y = max(on.T, on.B-h)
	}
	return winRect{x, y, x + w, y + h}, false
}
