//go:build windows

package main

import "syscall"

// sysProcHide 让子进程（ffmpeg/ffprobe）不弹控制台黑框
func sysProcHide() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
