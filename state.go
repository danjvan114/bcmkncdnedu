package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

/* ==========================================================================
   Edu —— 基于编程猫对象存储的分布式存储工具
   --------------------------------------------------------------------------
   本文件只管：程序目录 / 数据目录 / 设置项。
   账号在 accounts.go，上传在 job.go，切片在 media.go。
   ========================================================================== */

var (
	appDir  string // exe 所在目录
	dataDir string // <exe>/data
	webDir  string // <exe>/web
)

func initPaths() {
	exe, err := os.Executable()
	if err != nil {
		exe, _ = os.Getwd()
	}
	appDir = filepath.Dir(exe)
	dataDir = filepath.Join(appDir, "data")
	webDir = filepath.Join(appDir, "web")

	// 调试用：换数据目录 / 界面目录
	if v := os.Getenv("EDU_DATA"); v != "" {
		dataDir = v
	}
	if v := os.Getenv("EDU_WEB"); v != "" {
		webDir = v
	}
	_ = os.MkdirAll(dataDir, 0o755)
}

// settings 设置项：只有 m3u8 保存路径
type settings struct {
	M3U8Dir string `json:"m3u8Dir"`
}

var (
	curSettings = settings{}
	settingsMu  sync.Mutex
)

func settingsFile() string { return filepath.Join(dataDir, "settings.json") }

func loadSettings() {
	settingsMu.Lock()
	defer settingsMu.Unlock()

	curSettings = settings{M3U8Dir: filepath.Join(appDir, "m3u8")}
	if b, err := os.ReadFile(settingsFile()); err == nil {
		var s settings
		if json.Unmarshal(b, &s) == nil && s.M3U8Dir != "" {
			curSettings = s
		}
	}
	_ = os.MkdirAll(curSettings.M3U8Dir, 0o755)
}

func getSettings() settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return curSettings
}

func saveSettings(s settings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()

	if s.M3U8Dir == "" {
		s.M3U8Dir = filepath.Join(appDir, "m3u8")
	}
	if err := os.MkdirAll(s.M3U8Dir, 0o755); err != nil {
		return err
	}
	curSettings = s
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(settingsFile(), b, 0o644)
}

// tmpDir 切片临时目录（每次任务一个子目录，任务结束就删）
func tmpDir() string {
	d := filepath.Join(dataDir, "tmp")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// uniquePath 同名文件自动加 (2)(3)…
func uniquePath(dir, name string) string {
	ext := filepath.Ext(name)
	base := name[:len(name)-len(ext)]
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		return p
	}
	for i := 2; i < 1000; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(p); err != nil {
			return p
		}
	}
	return filepath.Join(dir, base+"-"+randHex(4)+ext)
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
