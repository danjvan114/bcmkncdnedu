package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

/* ==========================================================================
   Windows 原生交互：文件对话框 / 打开目录 / 标题栏深色
   全部用 syscall 直接调 user32/comdlg32/shell32/dwmapi，不引第三方库。
   ========================================================================== */

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	comdlg32 = syscall.NewLazyDLL("comdlg32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")

	procGetOpenFileName = comdlg32.NewProc("GetOpenFileNameW")
	procGetSaveFileName = comdlg32.NewProc("GetSaveFileNameW")
	procMessageBox      = user32.NewProc("MessageBoxW")

	procSHBrowseForFolder = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDL  = shell32.NewProc("SHGetPathFromIDListW")
	procShellExecute      = shell32.NewProc("ShellExecuteW")
	procExtractIconEx     = shell32.NewProc("ExtractIconExW")
	procSendMessage       = user32.NewProc("SendMessageW")
	procCoTaskMemFree     = ole32.NewProc("CoTaskMemFree")
	procCoInit            = ole32.NewProc("CoInitializeEx")
	procCoUninit          = ole32.NewProc("CoUninitialize")
	procDwmSetAttr        = dwmapi.NewProc("DwmSetWindowAttribute")
)

// mainHWND 是 WebView 窗口句柄，对话框拿它当 owner（否则会跑到窗口后面去）
var mainHWND uintptr

const (
	ofnReadOnly      = 0x00000001
	ofnOverwrite     = 0x00000002
	ofnHideReadOnly  = 0x00000004
	ofnNoChangeDir   = 0x00000008
	ofnAllowMulti    = 0x00000200
	ofnPathMustExist = 0x00000800
	ofnFileMustExist = 0x00001000
	ofnExplorer      = 0x00080000

	bifReturnOnlyFSDirs = 0x0001
	bifEditBox          = 0x0010
	bifNewDialogStyle   = 0x0040
)

// openFileNameW 对应 Win32 的 OPENFILENAMEW（64 位布局）
type openFileNameW struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	Flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	FlagsEx           uint32
}

type browseInfoW struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

// utf16z 把 "\x00" 分隔的字符串转成 UTF-16 双零结尾缓冲。
// syscall.UTF16PtrFromString 遇到内嵌 NUL 会报错，所以自己来。
func utf16z(s string) []uint16 {
	out := utf16.Encode([]rune(s))
	return append(out, 0, 0)
}

func ptrToU16(p *uint16) uintptr {
	if p == nil {
		return 0
	}
	return uintptr(unsafe.Pointer(p))
}

// openFilesDialog 多选文件。取消返回 (nil, nil)。
func openFilesDialog(title string, multi bool) ([]string, error) {
	buf := make([]uint16, 32768)
	filter := utf16z("所有文件\x00*.*\x00")
	titleZ := utf16z(title)
	if len(titleZ) > 1 {
		titleZ = titleZ[:len(titleZ)-1] // 单结尾
	}

	ofn := openFileNameW{
		lStructSize: uint32(unsafe.Sizeof(openFileNameW{})),
		hwndOwner:   mainHWND,
		lpstrFilter: &filter[0],
		nFilterIndex: 1,
		lpstrFile:   &buf[0],
		nMaxFile:    uint32(len(buf)),
		lpstrTitle:  &titleZ[0],
		Flags: ofnExplorer | ofnFileMustExist |
			ofnPathMustExist | ofnNoChangeDir | ofnHideReadOnly,
	}
	if multi {
		ofn.Flags |= ofnAllowMulti
	}
	r, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		return nil, nil // 用户取消
	}
	return parseMultiSelect(buf), nil
}

func parseMultiSelect(buf []uint16) []string {
	var parts []string
	start := 0
	for i, c := range buf {
		if c != 0 {
			continue
		}
		if i == start {
			break
		}
		parts = append(parts, string(utf16.Decode(buf[start:i])))
		start = i + 1
	}
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		return parts
	}
	// 多选时第一段是目录，后面是纯文件名
	dir := parts[0]
	out := make([]string, 0, len(parts)-1)
	for _, p := range parts[1:] {
		out = append(out, filepath.Join(dir, p))
	}
	return out
}

// saveFileDialog 另存为。取消返回 ""。
func saveFileDialog(title, defName string) (string, error) {
	buf := make([]uint16, 4096)
	copy(buf, utf16.Encode([]rune(defName)))

	filter := utf16z("所有文件\x00*.*\x00")
	titleZ := utf16z(title)
	if len(titleZ) > 1 {
		titleZ = titleZ[:len(titleZ)-1]
	}
	// 默认扩展名从文件名里取，让对话框自动补全
	var defExt *uint16
	if e := filepath.Ext(defName); len(e) > 1 {
		d := utf16z(e[1:])
		defExt = &d[0]
	}

	ofn := openFileNameW{
		lStructSize:   uint32(unsafe.Sizeof(openFileNameW{})),
		hwndOwner:     mainHWND,
		lpstrFilter:   &filter[0],
		nFilterIndex:  1,
		lpstrFile:     &buf[0],
		nMaxFile:      uint32(len(buf)),
		lpstrTitle:    &titleZ[0],
		lpstrDefExt:   defExt,
		Flags:         ofnExplorer | ofnOverwrite | ofnPathMustExist | ofnNoChangeDir | ofnHideReadOnly,
	}
	r, _, _ := procGetSaveFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		return "", nil
	}
	return syscall.UTF16ToString(buf), nil
}

// pickFolderDialog 选文件夹。取消返回 ""。
func pickFolderDialog(title string) (string, error) {
	nameBuf := make([]uint16, 1024)
	titleZ := utf16z(title)
	if len(titleZ) > 1 {
		titleZ = titleZ[:len(titleZ)-1]
	}
	bi := browseInfoW{
		hwndOwner:      mainHWND,
		pszDisplayName: &nameBuf[0],
		lpszTitle:      &titleZ[0],
		ulFlags:        bifReturnOnlyFSDirs | bifEditBox | bifNewDialogStyle,
	}
	pidl, _, _ := procSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "", nil
	}
	defer procCoTaskMemFree.Call(pidl)

	pathBuf := make([]uint16, 1024)
	ok, _, _ := procSHGetPathFromIDL.Call(pidl, uintptr(unsafe.Pointer(&pathBuf[0])))
	if ok == 0 {
		return "", errors.New("无法解析所选文件夹路径")
	}
	return syscall.UTF16ToString(pathBuf), nil
}

// shellOpen 用系统默认程序打开文件
func shellOpen(path string) error {
	verb := utf16z("open")
	file := utf16z(path)
	r, _, _ := procShellExecute.Call(0,
		ptrToU16(&verb[0]), ptrToU16(&file[0]), 0, 0, 1 /*SW_SHOWNORMAL*/)
	if r <= 32 {
		return errors.New("无法打开该文件")
	}
	return nil
}

// shellReveal 在资源管理器里选中该文件
func shellReveal(path string) error {
	if _, err := os.Stat(path); err != nil {
		// 文件没了就退一步，打开所在目录
		if dir := filepath.Dir(path); dir != "" {
			return exec.Command("explorer.exe", dir).Start()
		}
		return err
	}
	// explorer 的 /select 参数要求路径用引号包住，且不能走 cmd
	return exec.Command("explorer.exe", "/select,"+path).Start()
}

// setTitleBarDark 让 Windows 标题栏跟随应用主题（否则深色界面顶着一条白条）
func setTitleBarDark(dark bool) {
	setWindowDark(mainHWND, dark)
}

// setWindowDark 按窗口设置深色标题栏 / 边框（多窗口时按 hwnd 指定）
func setWindowDark(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	v := int32(0)
	if dark {
		v = 1
	}
	// DWMWA_USE_IMMERSIVE_DARK_MODE = 20（Win10 1809+）
	procDwmSetAttr.Call(hwnd, 20, uintptr(unsafe.Pointer(&v)), 4)
	// 顺手把标题栏颜色改成和界面一致的底色（Win11）
	var color int32
	if dark {
		color = 0x00201F1E // 0x00BBGGRR
	} else {
		color = 0x00F7F5F4
	}
	procDwmSetAttr.Call(hwnd, 35 /*DWMWA_CAPTION_COLOR*/, uintptr(unsafe.Pointer(&color)), 4)
}

func messageBox(title, text string, flags uintptr) {
	t := utf16z(title)
	m := utf16z(text)
	procMessageBox.Call(0, ptrToU16(&m[0]), ptrToU16(&t[0]), flags)
}

// setWindowIcon 把 exe 自己的图标资源挂到窗口上。
// 窗口类的图标是在 webview 库内部注册的，只能等窗口建好后再 WM_SETICON，
// 否则标题栏和任务栏显示的都是默认图标。
func setWindowIcon(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	p, perr := syscall.UTF16PtrFromString(exe)
	if perr != nil {
		return
	}
	var big, small uintptr
	n, _, _ := procExtractIconEx.Call(uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1)
	if n == 0 {
		return
	}
	const (
		wmSetIcon = 0x0080
		iconSmall = 0
		iconBig   = 1
	)
	if big != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconBig, big)
	}
	if small != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconSmall, small)
	}
}
