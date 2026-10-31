package main

import (
	"sync"
	"syscall"
	"unsafe"
)

/* ==========================================================================
   无边框窗口（自绘标题栏，类似 Electron 的 frame:false）
   --------------------------------------------------------------------------
   做法是给 go-webview2 创建出来的窗口换一个 WndProc：
     - WM_NCCALCSIZE 返回 0  → 干掉系统标题栏和非客户区，网页铺满整窗
     - WM_NCHITTEST          → 只有四边/四角算缩放区，其余交给网页
     - WM_GETMINMAXINFO      → 最小尺寸，免得拖成一条线
     - WM_APP+1              → 网页点住标题栏时触发系统「拖动窗口」
   拖动必须走自定义消息：WM_NCLBUTTONDOWN 的移动循环要求 ReleaseCapture
   在窗口所属线程（也就是 WebView2 的消息循环线程）上执行，HTTP 处理协程
   直接调是不生效的。
   ========================================================================== */

const (
	wmNCCalcSize   = 0x0083
	wmNCHitTest    = 0x0084
	wmGetMinMax    = 0x0024
	wmNCLButtonDn  = 0x00A1
	wmAppDrag      = 0x8001 // WM_APP + 1
	htClient       = 1
	htCaption      = 2
	htLeft         = 10
	htRight        = 11
	htTop          = 12
	htTopLeft      = 13
	htTopRight     = 14
	htBottom       = 15
	htBottomLeft   = 16
	htBottomRight  = 17
	gwlStyle       = ^uintptr(15) // -16
	gwlpWndProc    = ^uintptr(3)  // -4
	wsCaption      = 0x00C00000
	wsThickFrame   = 0x00040000
	wsMinimizeBox  = 0x00020000
	wsMaximizeBox  = 0x00010000
	wsSysMenu      = 0x00080000
	swpFrameChange = 0x0020
	swpNoMove      = 0x0002
	swpNoSize      = 0x0001
	swpNoZOrder    = 0x0004
	resizeBorder   = 6
)

var (
	procDwmExtendFrame = dwmapi.NewProc("DwmExtendFrameIntoClientArea")

	procGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procCallWindowProc   = user32.NewProc("CallWindowProcW")
	procReleaseCapture   = user32.NewProc("ReleaseCapture")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procIsZoomed         = user32.NewProc("IsZoomed")
	procIsIconic         = user32.NewProc("IsIconic")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procMonitorFromWnd   = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfo   = user32.NewProc("GetMonitorInfoW")

	// ⚠️ 一个进程里可能有多个无边框窗口（主窗口 + 传输列表窗口），
	// 所以「旧 WndProc」和「最小尺寸」必须按 hwnd 存，不能用单个全局变量。
	frameMu    sync.Mutex
	oldProcs   = map[uintptr]uintptr{}
	minSizes   = map[uintptr][2]int32{}
)

type winPoint struct {
	X, Y int32
}

// pointOf 从 lParam 取出带符号的屏幕坐标（多显示器下可能为负，
// ⚠️ 不能用 LOWORD/HIWORD，那两个当无符号算）
func pointOf(lparam uintptr) winPoint {
	return winPoint{
		X: int32(int16(lparam & 0xffff)),
		Y: int32(int16((lparam >> 16) & 0xffff)),
	}
}

// hitCode 判断点落在窗口 rect 的哪条边/哪个角上，不在 6px 环内返回 0
func hitCode(r winRect, p winPoint) uintptr {
	left := p.X < r.Left+resizeBorder
	right := p.X >= r.Right-resizeBorder
	top := p.Y < r.Top+resizeBorder
	bottom := p.Y >= r.Bottom-resizeBorder
	switch {
	case top && left:
		return htTopLeft
	case top && right:
		return htTopRight
	case bottom && left:
		return htBottomLeft
	case bottom && right:
		return htBottomRight
	case left:
		return htLeft
	case right:
		return htRight
	case top:
		return htTop
	case bottom:
		return htBottom
	}
	return 0
}

type winRect struct {
	Left, Top, Right, Bottom int32
}

type dwmMargins struct {
	Left, Right, Top, Bottom int32
}

type monitorInfo struct {
	Size    uint32
	Monitor winRect
	Work    winRect
	Flags   uint32
}

type minMaxInfo struct {
	Reserved   [2]int32
	MaxSize    [2]int32
	MaxPos     [2]int32
	MinTrack   [2]int32
	MaxTrack   [2]int32
}

// installFrame 把窗口变成「无系统标题栏」的样子
func installFrame(hwnd uintptr, minW, minH int32) {
	// ⚠️ 不要删 WS_CAPTION —— 保留完整的标准窗口样式，Windows 才会把它当
	// 原生窗口对待（最小化/最大化动画、贴边分屏、Alt+Tab 缩略图都靠这个）。
	// 标题栏是靠 WM_NCCALCSIZE 返回 0「不画非客户区」藏掉的。
	style, _, _ := procGetWindowLongPtr.Call(hwnd, gwlStyle)
	style |= wsCaption | wsThickFrame | wsMinimizeBox | wsMaximizeBox | wsSysMenu
	procSetWindowLongPtr.Call(hwnd, gwlStyle, style)

	frameMu.Lock()
	minSizes[hwnd] = [2]int32{minW, minH}
	frameMu.Unlock()

	// ⚠️ 顺序很重要：必须先挂上自己的 WndProc，再 SWP_FRAMECHANGED 触发
	// WM_NCCALCSIZE。反过来的话重算时还是系统的 WndProc，原生标题栏会留在
	// 窗口上，一直到用户点最大化才「突然」变正常（这个 bug 踩过）。
	cb := syscall.NewCallback(frameWndProc)
	old, _, _ := procSetWindowLongPtr.Call(hwnd, gwlpWndProc, cb)
	frameMu.Lock()
	oldProcs[hwnd] = old
	frameMu.Unlock()

	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpFrameChange|swpNoMove|swpNoSize|swpNoZOrder)

	applyDwmEffects(hwnd)
}

// applyDwmEffects 圆角 + 阴影。
// 把 DWM 边框延伸 1px 进来，DWM 就会照常给窗口画投影（NCCALCSIZE 归零后
// 系统原本的边框/阴影都没了，这是补回来的标准做法）。
func applyDwmEffects(hwnd uintptr) {
	m := dwmMargins{Left: 1, Right: 1, Top: 1, Bottom: 1}
	procDwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&m)))

	// Win11：DWMWA_WINDOW_CORNER_PREFERENCE(33) = DWMWCP_ROUND(2)
	pref := int32(2)
	procDwmSetAttr.Call(hwnd, 33, uintptr(unsafe.Pointer(&pref)), 4)
	// Win11：深色标题栏（虽然看不见标题栏，但边框阴影颜色会跟着走）
}

func isZoomed(hwnd uintptr) bool {
	r, _, _ := procIsZoomed.Call(hwnd)
	return r != 0
}

func frameWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmNCCalcSize:
		if wparam == 1 {
			// 最大化时窗口会比工作区大一圈（四周各多出一个边框），
			// 不修正的话网页底部/右侧会被裁掉。
			if isZoomed(hwnd) {
				params := (*[3]winRect)(unsafe.Pointer(lparam))
				mon, _, _ := procMonitorFromWnd.Call(hwnd, 2 /*MONITOR_DEFAULTTONEAREST*/)
				if mon != 0 {
					var mi monitorInfo
					mi.Size = uint32(unsafe.Sizeof(mi))
					if ok, _, _ := procGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); ok != 0 {
						params[0] = mi.Work
					}
				}
			}
			return 0
		}

	case wmNCHitTest:
		if isZoomed(hwnd) {
			return htClient
		}
		var r winRect
		if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
			break
		}
		if code := hitCode(r, pointOf(lparam)); code != 0 {
			return code
		}
		return htClient

	case wmGetMinMax:
		mmi := (*minMaxInfo)(unsafe.Pointer(lparam))
		frameMu.Lock()
		mn := minSizes[hwnd]
		frameMu.Unlock()
		if mn[0] > 0 {
			mmi.MinTrack[0] = mn[0]
			mmi.MinTrack[1] = mn[1]
		}
		// ⚠️ 关键：不设 MaxSize/MaxPos 的话，最大化会按「整个屏幕」算，
		// 窗口比工作区大一圈，把任务栏盖住（NCCALCSIZE 归零后连边框都没了）。
		// 这里明确把最大化尺寸钉到工作区。
		mon, _, _ := procMonitorFromWnd.Call(hwnd, 2 /*MONITOR_DEFAULTTONEAREST*/)
		if mon != 0 {
			var mi monitorInfo
			mi.Size = uint32(unsafe.Sizeof(mi))
			if ok, _, _ := procGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); ok != 0 {
				mmi.MaxPos[0] = mi.Work.Left - mi.Monitor.Left
				mmi.MaxPos[1] = mi.Work.Top - mi.Monitor.Top
				mmi.MaxSize[0] = mi.Work.Right - mi.Work.Left
				mmi.MaxSize[1] = mi.Work.Bottom - mi.Work.Top
			}
		}

	case wmAppDrag:
		// 这一句必须在窗口线程上执行
		procReleaseCapture.Call()
		procSendMessageW.Call(hwnd, wmNCLButtonDn, htCaption, 0)
		return 0

	case wmAppResize:
		// 同上：先松掉 Chromium 子窗口握着的捕获，再让系统进入缩放循环
		if wparam != 0 {
			procReleaseCapture.Call()
			procSendMessageW.Call(hwnd, wmNCLButtonDn, wparam, 0)
		}
		return 0
	}

	frameMu.Lock()
	old := oldProcs[hwnd]
	frameMu.Unlock()
	if old != 0 {
		r, _, _ := procCallWindowProc.Call(old, hwnd, msg, wparam, lparam)
		return r
	}
	return 0
}

// ---- 窗口操作（给前端按钮用） ----
//
// ⚠️ 别用 ShowWindow(SW_MAXIMIZE)：从别的线程调只会把 zoom 标记置上，
// 窗口尺寸不变（实测）。走 WM_SYSCOMMAND 由窗口线程自己处理才正常。

const (
	wmSysCommand = 0x0112
	scMinimize   = 0xF020
	scMaximize   = 0xF030
	scRestore    = 0xF120
)

func winMinimize(hwnd uintptr) {
	procSendMessageW.Call(hwnd, wmSysCommand, scMinimize, 0)
}

func winToggleMax(hwnd uintptr) bool {
	if isZoomed(hwnd) {
		procSendMessageW.Call(hwnd, wmSysCommand, scRestore, 0)
		return false
	}
	procSendMessageW.Call(hwnd, wmSysCommand, scMaximize, 0)
	return true
}

func winClose(hwnd uintptr) {
	procPostMessageW.Call(hwnd, 0x0010, 0, 0) // WM_CLOSE
}

func hideWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, 0) // SW_HIDE
}

func showWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, 5) // SW_SHOW
}

// winStartDrag 让窗口跟着鼠标走（网页标题栏按下时调用）
func winStartDrag(hwnd uintptr) {
	procPostMessageW.Call(hwnd, wmAppDrag, 0, 0)
}

// focusWindow 把已经开着的窗口叫到前面（最小化过就先还原）
func focusWindow(hwnd uintptr) {
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindow.Call(hwnd, 9) // SW_RESTORE
	}
	procSetForeground.Call(hwnd)
}

// moveWindow 只挪位置，不改尺寸
func moveWindow(hwnd uintptr, x, y int32) {
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder)
}

/* ==========================================================================
   让无边框窗口还能拖动边缘改大小
   --------------------------------------------------------------------------
   根因：WebView2 的子窗口铺满整个客户区（连最外面那圈边框也盖住），鼠标落在
   边上时 WM_NCHITTEST 由子窗口接走，主窗口的 frameWndProc 永远收不到 →
   光标不变、也拖不动。这是「子窗口集成法」的已知缺陷，微软的解法是改用
   UI 合成器（WinRT Composition），go-webview2 走不了那条路。

   ⚠️ 两条死路都试过，别再回头：
     1. 给子窗口挂 WndProc 返回 HTTRANSPARENT(-1) —— 无效。MSDN 原文是
        「消息会发给同一线程中**位于它下面的窗口**」，父窗口不算「下面」，
        不会冒泡上去。
     2. 在子窗口的 WM_LBUTTONDOWN 里转发 WM_NCLBUTTONDOWN 给主窗口 —— 无效。
        缩放循环要求鼠标捕获在主窗口手上，而按下时捕获在 Chromium 子窗口，
        必须先在窗口线程上 ReleaseCapture 才行，子窗口拿到的已经是晚的了。

   所以改成：**页面自己铺一圈热区**（ui/src/components/ResizeFrame.vue），
   mousedown 时调 /api/window/resize 告诉 Go 要拉哪条边，Go 在窗口线程上
   ReleaseCapture + SendMessage(WM_NCLBUTTONDOWN, HTLEFT…) 让系统自己进入
   缩放循环。和标题栏拖动（wmAppDrag + HTCAPTION）完全同一套机制。
   ========================================================================== */

const wmAppResize = 0x8003 // WM_APP + 3

// winStartResize 按命中码开始拖拽缩放（HTLEFT/HTTOP/…）
func winStartResize(hwnd uintptr, code uintptr) {
	procPostMessageW.Call(hwnd, wmAppResize, code, 0)
}

// resizeCodeOf 把前端的边名换成命中码
func resizeCodeOf(edge string) uintptr {
	switch edge {
	case "left":
		return htLeft
	case "right":
		return htRight
	case "top":
		return htTop
	case "bottom":
		return htBottom
	case "topleft":
		return htTopLeft
	case "topright":
		return htTopRight
	case "bottomleft":
		return htBottomLeft
	case "bottomright":
		return htBottomRight
	}
	return 0
}
