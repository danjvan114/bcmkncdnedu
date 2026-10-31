//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jchv/go-webview2"
)

/* ==========================================================================
   Edu —— 基于编程猫对象存储的分布式存储工具
   --------------------------------------------------------------------------
   桌面壳：Go + WebView2（jchv/go-webview2，纯 syscall 调 COM，不需要 CGO）
   界面：Vue 3，构建产物放 <exe>/web
   数据：<exe>/data（accounts.json / settings.json / tmp）
   ========================================================================== */

func main() {
	// WebView2 的窗口和消息循环必须待在同一个 OS 线程上
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	initPaths()
	loadSettings()
	loadAccounts()

	addr, err := startServer()
	if err != nil {
		messageBox("Edu", "本地服务启动失败：\n"+err.Error(), 0x10)
		return
	}

	// 只起服务、不开窗口 —— 用浏览器调前端时用（EDU_NO_WINDOW=1）
	if os.Getenv("EDU_NO_WINDOW") != "" {
		fmt.Println("serving on", addr)
		select {}
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  filepath.Join(dataDir, "webview"),
		WindowOptions: webview2.WindowOptions{
			Title:  "Edu 分布式存储",
			Width:  1080,
			Height: 660, // 本机工作区只有 1280×680，别再高了
			Center: true,
		},
	})
	if w == nil {
		messageBox("Edu",
			"无法启动 WebView2。\n\n请先安装 Microsoft Edge WebView2 Runtime：\nhttps://go.microsoft.com/fwlink/p/?LinkId=2124703",
			0x10)
		return
	}
	defer w.Destroy()

	mainHWND = uintptr(w.Window())
	// 先藏起来：创建那一刻带系统标题栏，改完再显示，避免闪一下原生按钮
	hideWindow(mainHWND)
	setWindowIcon(mainHWND)
	installFrame(mainHWND, 880, 560)
	showWindow(mainHWND)
	setTitleBarDark(false)

	w.Navigate(addr)
	w.Run()
}
