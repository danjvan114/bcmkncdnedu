package main

import (
	"bufio"
	"bytes"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

/* ==========================================================================
   媒体探测 + ffmpeg 切片
   --------------------------------------------------------------------------
   目标：把任意视频切成一段段 <40MB 的 TS 片段（HLS），每段单独传 CDN。
   - 优先 -c copy（不转码，秒级完成，画质无损）
   - 切片时长按码率反推，尽量让每段 30MB 左右
   - copy 切出来的段受关键帧限制，万一超过 45MB，自动退回「强制关键帧转码」再切一次
   ========================================================================== */

const (
	segTargetBytes = 30 << 20 // 每段目标大小
	segMaxBytes    = 45 << 20 // 超过这个就认为切失败了（桶上限 50MB）
)

type mediaInfo struct {
	Duration float64
	HasVideo bool
	HasAudio bool
	Width    int
	Height   int
}

type hlsSegment struct {
	Name string
	Path string
	Size int64
	Dur  float64
}

// ---------------------------------------------------------------------------
// ffmpeg 定位
// ---------------------------------------------------------------------------

func ffmpegPath() string {
	if v := os.Getenv("EDU_FFMPEG"); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v
		}
	}
	exeName := "ffmpeg"
	if runtime.GOOS == "windows" {
		exeName = "ffmpeg.exe"
	}
	cands := []string{
		filepath.Join(appDir, exeName),
		filepath.Join(appDir, "ffmpeg", exeName),
		`D:\app\ffmpeg\bin\ffmpeg.exe`,
		`C:\ffmpeg\bin\ffmpeg.exe`,
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

func ffprobePath() string {
	ff := ffmpegPath()
	if ff == "" {
		return ""
	}
	dir := filepath.Dir(ff)
	name := "ffprobe"
	if runtime.GOOS == "windows" {
		name = "ffprobe.exe"
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	return ""
}

// ---------------------------------------------------------------------------
// 探测
// ---------------------------------------------------------------------------

func probeMedia(path string) (*mediaInfo, error) {
	info := &mediaInfo{}
	pp := ffprobePath()
	if pp == "" {
		return info, errors.New("没找到 ffprobe，请把 ffmpeg 放到程序目录或设置 EDU_FFMPEG")
	}
	cmd := exec.Command(pp, "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path)
	hideCmdWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return info, errors.New("ffprobe 读不了这个文件（不是媒体？）")
	}
	var r struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return info, errors.New("ffprobe 输出解析失败")
	}
	if d, err := strconv.ParseFloat(r.Format.Duration, 64); err == nil {
		info.Duration = d
	}
	for _, s := range r.Streams {
		switch s.CodecType {
		case "video":
			info.HasVideo = true
			if s.Width > 0 {
				info.Width, info.Height = s.Width, s.Height
			}
		case "audio":
			info.HasAudio = true
		}
	}
	return info, nil
}

// ---------------------------------------------------------------------------
// 切片
// ---------------------------------------------------------------------------

// splitHLS 把 src 切成 HLS(TS) 放到 outDir，返回片段列表（按播放顺序）。
// reencode=true 时强制关键帧重编码（慢，但一定能切出目标时长）。
func splitHLS(src, outDir string, segSeconds float64, reencode bool, onLog func(string)) ([]hlsSegment, error) {
	ff := ffmpegPath()
	if ff == "" {
		return nil, errors.New("没找到 ffmpeg，请把 ffmpeg.exe 放到程序目录")
	}
	if segSeconds < 1 {
		segSeconds = 10
	}
	_ = os.MkdirAll(outDir, 0o755)

	playlist := filepath.Join(outDir, "index.m3u8")
	segPat := filepath.Join(outDir, "p_%05d.ts")

	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", src}
	if reencode {
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "20",
			"-force_key_frames", "expr:gte(t,n_forced*"+strconv.FormatFloat(segSeconds, 'f', -1, 64)+")",
			"-c:a", "aac", "-b:a", "128k")
	} else {
		args = append(args, "-c", "copy")
	}
	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.FormatFloat(segSeconds, 'f', -1, 64),
		"-hls_playlist_type", "vod",
		"-hls_list_size", "0",
		"-hls_segment_filename", segPat,
		playlist)

	cmd := exec.Command(ff, args...)
	hideCmdWindow(cmd)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if onLog != nil {
		onLog("ffmpeg 切片中…")
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New("切片失败：" + clipStr(lastLines(msg, 3), 300))
	}

	segs, err := parseLocalM3U8(playlist)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, errors.New("切片结果为空")
	}
	return segs, nil
}

func parseLocalM3U8(p string) ([]hlsSegment, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []hlsSegment
	dir := filepath.Dir(p)
	dur := 0.0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			v := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.Index(v, ","); i >= 0 {
				v = v[:i]
			}
			dur, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
		case line != "" && !strings.HasPrefix(line, "#"):
			full := filepath.Join(dir, filepath.FromSlash(line))
			st, err := os.Stat(full)
			if err != nil {
				continue
			}
			out = append(out, hlsSegment{Name: filepath.Base(full), Path: full, Size: st.Size(), Dur: dur})
			dur = 0
		}
	}
	return out, sc.Err()
}

// segNameChars 片段名的字符表：小写字母 + 数字
const segNameChars = "abcdefghijklmnopqrstuvwxyz0123456789"

// randSegName 生成 12 位随机名字（形如 a5rawesg6352.ts）。
// 不用序号、不用哈希：每个片段名字都是随机打的，跟顺序无关，
// 从文件名反推不出这是第几片、更猜不到同批的其他片段。
func randSegName() string {
	buf := make([]byte, 12)
	if _, err := crand.Read(buf); err != nil {
		// 极端情况下退化成时间戳，反正不会重名
		return strconv.FormatInt(time.Now().UnixNano(), 36) + ".ts"
	}
	for i := range buf {
		buf[i] = segNameChars[int(buf[i])%len(segNameChars)]
	}
	return string(buf) + ".ts"
}

func maxSegSize(segs []hlsSegment) int64 {
	var m int64
	for _, s := range segs {
		if s.Size > m {
			m = s.Size
		}
	}
	return m
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], " ")
}

// hideCmdWindow 别让 ffmpeg 在 Windows 上弹黑框
func hideCmdWindow(cmd *exec.Cmd) {
	if runtime.GOOS != "windows" {
		return
	}
	cmd.SysProcAttr = sysProcHide()
}
