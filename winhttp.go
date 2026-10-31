package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

/* ==========================================================================
   下载：必须走 WinHTTP，不能用 net/http
   --------------------------------------------------------------------------
   creation.bcmcdn.com 前面是腾讯 EdgeOne，它按 **TLS 指纹（JA3/JA4）** 判客户端。
   Go 的 crypto/tls 指纹被直接判成机器人，返回的是一段几百字节的混淆 JS
   挑战页（状态码还是 200！），表现为「文件下载下来是坏的 / zip 解不开」。
   换 UA、换头顺序、强制 HTTP/1.1、带 EO_Bot_Ssid cookie 全部无效。

   WinHTTP 走的是 Schannel（和 curl / Edge 同一套 TLS 栈），指纹正常 → 能过。
   ⚠️ 别为了「统一代码风格」把它改回 net/http。
   ========================================================================== */

var (
	winhttpDLL          = syscall.NewLazyDLL("winhttp.dll")
	procWinHttpOpen     = winhttpDLL.NewProc("WinHttpOpen")
	procWinHttpConnect  = winhttpDLL.NewProc("WinHttpConnect")
	procWinHttpOpenReq  = winhttpDLL.NewProc("WinHttpOpenRequest")
	procWinHttpSend     = winhttpDLL.NewProc("WinHttpSendRequest")
	procWinHttpRecv     = winhttpDLL.NewProc("WinHttpReceiveResponse")
	procWinHttpQueryHdr = winhttpDLL.NewProc("WinHttpQueryHeaders")
	procWinHttpReadData = winhttpDLL.NewProc("WinHttpReadData")
	procWinHttpClose    = winhttpDLL.NewProc("WinHttpCloseHandle")
)

const (
	winhttpAccessNoProxy = 1
	winhttpFlagSecure    = 0x00800000
	winhttpQueryStatus   = 19
	winhttpQueryLocation = 33
	winhttpQueryLength   = 5
	winhttpQueryNumber   = 0x20000000
)

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// winhttpGet 把 rawURL 下载到 dst，返回字节数。onProgress(已下载, 总长)，总长未知时为 0。
func winhttpGet(rawURL, dst string, onProgress func(int64, int64)) (int64, error) {
	return winhttpGetDepth(rawURL, dst, onProgress, 0)
}

func winhttpGetDepth(rawURL, dst string, onProgress func(int64, int64), depth int) (int64, error) {
	if depth > 3 {
		return 0, errors.New("重定向次数过多")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, err
	}
	host := u.Hostname()
	port := uintptr(443)
	if u.Scheme == "http" {
		port = 80
	}
	if ps := u.Port(); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil {
			port = uintptr(v)
		}
	}
	obj := u.EscapedPath()
	if obj == "" {
		obj = "/"
	}
	if u.RawQuery != "" {
		obj += "?" + u.RawQuery
	}

	sess, _, _ := procWinHttpOpen.Call(uintptr(unsafe.Pointer(utf16Ptr("PickcatDrive/1.0"))), winhttpAccessNoProxy, 0, 0, 0)
	if sess == 0 {
		return 0, errors.New("WinHttpOpen 失败")
	}
	defer procWinHttpClose.Call(sess)

	conn, _, _ := procWinHttpConnect.Call(sess, uintptr(unsafe.Pointer(utf16Ptr(host))), port, 0)
	if conn == 0 {
		return 0, fmt.Errorf("连不上 %s", host)
	}
	defer procWinHttpClose.Call(conn)

	flags := uintptr(0)
	if u.Scheme == "https" {
		flags = winhttpFlagSecure
	}
	req, _, _ := procWinHttpOpenReq.Call(conn,
		uintptr(unsafe.Pointer(utf16Ptr("GET"))),
		uintptr(unsafe.Pointer(utf16Ptr(obj))),
		0, 0, 0, flags)
	if req == 0 {
		return 0, errors.New("WinHttpOpenRequest 失败")
	}
	defer procWinHttpClose.Call(req)

	if r, _, _ := procWinHttpSend.Call(req, 0, 0, 0, 0, 0, 0); r == 0 {
		return 0, errors.New("请求发送失败（网络不可用？）")
	}
	if r, _, _ := procWinHttpRecv.Call(req, 0); r == 0 {
		return 0, errors.New("服务器没有响应")
	}

	var status uint32
	sz := uint32(4)
	procWinHttpQueryHdr.Call(req, winhttpQueryStatus|winhttpQueryNumber, 0,
		uintptr(unsafe.Pointer(&status)), uintptr(unsafe.Pointer(&sz)), 0)

	switch status {
	case 301, 302, 303, 307, 308:
		loc := winhttpHeaderString(req, winhttpQueryLocation)
		if loc == "" {
			return 0, fmt.Errorf("HTTP %d 但没有 Location", status)
		}
		next, err := u.Parse(loc)
		if err != nil {
			return 0, err
		}
		return winhttpGetDepth(next.String(), dst, onProgress, depth+1)
	case 200:
		// ok
	case 404:
		return 0, errors.New("云端对象不存在（可能已被删除）")
	case 403:
		return 0, errors.New("云端拒绝访问（链接可能已失效）")
	default:
		return 0, fmt.Errorf("下载失败：HTTP %d", status)
	}

	var total uint64
	sz = 8
	procWinHttpQueryHdr.Call(req, winhttpQueryLength|winhttpQueryNumber, 0,
		uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&sz)), 0)

	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buf := make([]byte, 128*1024)
	var got int64
	for {
		var n uint32
		r, _, _ := procWinHttpReadData.Call(req,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
			uintptr(unsafe.Pointer(&n)))
		if r == 0 {
			if got == 0 {
				return 0, errors.New("读取数据失败")
			}
			break
		}
		if n == 0 {
			break
		}
		if _, err := f.Write(buf[:n]); err != nil {
			return got, err
		}
		got += int64(n)
		if onProgress != nil {
			onProgress(got, int64(total))
		}
	}
	if err := f.Close(); err != nil {
		return got, err
	}
	if total > 0 && uint64(got) != total {
		return got, fmt.Errorf("下载不完整（%d/%d 字节）", got, total)
	}
	return got, nil
}

func winhttpHeaderString(req uintptr, level uintptr) string {
	buf := make([]uint16, 2048)
	sz := uint32(len(buf) * 2)
	r, _, _ := procWinHttpQueryHdr.Call(req, level, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&sz)), 0)
	if r == 0 || sz == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
