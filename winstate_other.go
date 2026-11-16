//go:build desktop && !windows

package main

// 非 Windows 桌面构建（未发布，仅保证可编译）：用 Wails 运行时接口尽力而为

import (
	"context"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const winClassName = ""

func captureWindow(ctx context.Context) (WindowState, bool) {
	if ctx == nil {
		return WindowState{}, false
	}
	x, y := wruntime.WindowGetPosition(ctx)
	w, h := wruntime.WindowGetSize(ctx)
	return WindowState{X: x, Y: y, W: w, H: h, Maximised: wruntime.WindowIsMaximised(ctx)}, true
}

func applyWindow(ctx context.Context, ws WindowState, minW, minH int) (winRect, bool, bool) {
	var works []winRect
	var primary winRect
	if screens, err := wruntime.ScreenGetAll(ctx); err == nil {
		for _, s := range screens {
			if s.IsPrimary || primary.w() == 0 {
				primary = winRect{0, 0, s.Size.Width, s.Size.Height}
			}
		}
	}
	if primary.w() == 0 {
		return winRect{}, false, false
	}
	works = append(works, primary)
	r, centered := placeWindow(ws, works, primary, minW, minH)
	wruntime.WindowSetSize(ctx, r.w(), r.h())
	wruntime.WindowSetPosition(ctx, r.L, r.T)
	return r, centered, true
}
