//go:build desktop && windows

package main

// Windows 下用 Win32 API 直接取/设窗口矩形（物理像素、屏幕坐标），原因：
// Wails v2 的 WindowGetPosition 返回屏幕坐标，WindowSetPosition 却是相对“当前所在显示器工作区”的坐标，
// 多显示器时两者不对称，无法原样恢复到副屏。窗口类名固定为 winClassName，用 FindWindowW 找到窗口句柄。

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

const winClassName = "jt808simWindow"

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procGetWindowPlacement  = user32.NewProc("GetWindowPlacement")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procIsZoomed            = user32.NewProc("IsZoomed")
	procIsIconic            = user32.NewProc("IsIconic")
	procIsWindow            = user32.NewProc("IsWindow")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procMonitorFromPoint    = user32.NewProc("MonitorFromPoint")
	procMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
)

type rect32 struct{ L, T, R, B int32 }

type monitorInfo struct {
	Size    uint32
	Monitor rect32
	Work    rect32
	Flags   uint32
}

type windowPlacement struct {
	Length     uint32
	Flags      uint32
	ShowCmd    uint32
	MinPos     [2]int32
	MaxPos     [2]int32
	NormalPos  rect32
	DeviceRect rect32
}

func (r rect32) rect() winRect { return winRect{int(r.L), int(r.T), int(r.R), int(r.B)} }

func findMainWindow() uintptr {
	cls, _ := windows.UTF16PtrFromString(winClassName)
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	if h != 0 {
		if ok, _, _ := procIsWindow.Call(h); ok == 0 {
			return 0
		}
	}
	return h
}

func monitorWork(hmon uintptr) (work, mon winRect, ok bool) {
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if r, _, _ := procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return winRect{}, winRect{}, false
	}
	return mi.Work.rect(), mi.Monitor.rect(), true
}

// monitors 全部显示器工作区与主显示器（工作区、整屏）
func monitors() (works []winRect, primaryWork, primaryMon winRect) {
	cb := windows.NewCallback(func(hmon, hdc, lprc, data uintptr) uintptr {
		if w, _, ok := monitorWork(hmon); ok {
			works = append(works, w)
		}
		return 1
	})
	procEnumDisplayMonitors.Call(0, 0, cb, 0)
	const monitorDefaultToPrimary = 1
	hp, _, _ := procMonitorFromPoint.Call(0, monitorDefaultToPrimary) // POINT{0,0} 按值传递（x、y 各 32 位）
	primaryWork, primaryMon, _ = monitorWork(hp)
	if len(works) == 0 && primaryWork.w() > 0 {
		works = []winRect{primaryWork}
	}
	return
}

// captureWindow 当前窗口状态；最大化 / 最小化时取“还原”矩形（GetWindowPlacement 为工作区坐标，换算成屏幕坐标）
func captureWindow(_ context.Context) (WindowState, bool) {
	h := findMainWindow()
	if h == 0 {
		return WindowState{}, false
	}
	zoomed, _, _ := procIsZoomed.Call(h)
	iconic, _, _ := procIsIconic.Call(h)
	var r winRect
	if zoomed == 0 && iconic == 0 {
		var rc rect32
		if ok, _, _ := procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&rc))); ok == 0 {
			return WindowState{}, false
		}
		r = rc.rect()
	} else {
		wp := windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{}))}
		if ok, _, _ := procGetWindowPlacement.Call(h, uintptr(unsafe.Pointer(&wp))); ok == 0 {
			return WindowState{}, false
		}
		// NormalPos 是工作区坐标：用窗口实际所在的显示器换算（最大化可能在副屏），失败退回主显示器
		const monitorDefaultToNearest = 2
		hm, _, _ := procMonitorFromWindow.Call(h, monitorDefaultToNearest)
		pw, pm, ok := monitorWork(hm)
		if !ok {
			_, pw, pm = monitors()
		}
		dx, dy := pw.L-pm.L, pw.T-pm.T
		n := wp.NormalPos.rect()
		r = winRect{n.L + dx, n.T + dy, n.R + dx, n.B + dy}
	}
	return WindowState{X: r.L, Y: r.T, W: r.w(), H: r.h(), Maximised: zoomed != 0}, true
}

// applyWindow 把还原矩形设到窗口上（窗口此时可能尚未显示）；最大化由 options.WindowStartState 处理
func applyWindow(_ context.Context, ws WindowState, minW, minH int) (winRect, bool, bool) {
	h := findMainWindow()
	if h == 0 {
		return winRect{}, false, false
	}
	works, pw, _ := monitors()
	if pw.w() <= 0 {
		return winRect{}, false, false
	}
	r, centered := placeWindow(ws, works, pw, minW, minH)
	const swpNoZOrder, swpNoActivate = 0x0004, 0x0010
	ok, _, _ := procSetWindowPos.Call(h, 0, uintptr(r.L), uintptr(r.T), uintptr(r.w()), uintptr(r.h()), swpNoZOrder|swpNoActivate)
	return r, centered, ok != 0
}
