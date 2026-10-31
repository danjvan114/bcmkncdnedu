package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

/* ==========================================================================
   本地 HTTP 服务（只监听 127.0.0.1）
   --------------------------------------------------------------------------
   界面和「后端」在同一个进程里：Go 起服务 → WebView2 加载 127.0.0.1:5301，
   前端 fetch 同源接口。界面不 embed，产物就是 <exe>/web 里的静态文件。
   ⚠️ 端口决定 localStorage 的「源」，别随便改。
   ========================================================================== */

const fixedPort = "5301"

var localBase string

func startServer() (string, error) {
	addr := "127.0.0.1:" + fixedPort
	if p := os.Getenv("EDU_PORT"); p != "" {
		addr = "127.0.0.1:" + p
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil && os.Getenv("EDU_PORT") == "" {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return "", err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/", apiHandler)
	mux.Handle("/", staticHandler())

	go func() {
		_ = http.Serve(ln, mux)
	}()
	localBase = "http://" + ln.Addr().String() + "/"
	return localBase, nil
}

func staticHandler() http.Handler {
	fs := http.FileServer(http.Dir(webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		full := filepath.Join(webDir, filepath.FromSlash(p))
		if st, err := os.Stat(full); err != nil || st.IsDir() {
			idx := filepath.Join(webDir, "index.html")
			data, rerr := os.ReadFile(idx)
			if rerr != nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8">
<body style="font:14px/1.9 system-ui,'Microsoft YaHei';padding:48px;color:#333">
<h3>界面文件缺失</h3>
<p>没找到 <code>` + idx + `</code></p>
<p>请把前端构建产物（index.html + assets/）放到 exe 同级的 <b>web</b> 目录里。</p>
</body>`))
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(data)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		fs.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func okJSON(w http.ResponseWriter, extra map[string]any) {
	m := map[string]any{"ok": true}
	for k, v := range extra {
		m[k] = v
	}
	writeJSON(w, m)
}

func errJSON(w http.ResponseWriter, err error) {
	writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
}

func apiHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api")

	var body map[string]any
	if r.Method == "POST" {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	str := func(k string) string {
		if body == nil {
			return ""
		}
		if v, ok := body[k].(string); ok {
			return v
		}
		return ""
	}
	strList := func(k string) []string {
		if body == nil {
			return nil
		}
		arr, ok := body[k].([]any)
		if !ok {
			return nil
		}
		out := make([]string, 0, len(arr))
		for _, v := range arr {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}

	switch path {
	case "/state":
		total, okn, failn, pend := accountStats()
		busy, done, vtotal := verifyProgress()
		ff := ffmpegPath()
		okJSON(w, map[string]any{
			"accounts": accountsView(),
			"stats": map[string]int{
				"total": total, "ok": okn, "fail": failn, "pending": pend,
			},
			"verify": map[string]any{"busy": busy, "done": done, "total": vtotal},
			"settings": getSettings(),
			"ffmpeg":   ff,
			"job":      jobSnapshot(),
			"busy":     isJobBusy(),
			"time":     time.Now().UnixMilli(),
		})

	case "/job":
		okJSON(w, map[string]any{"job": jobSnapshot(), "busy": isJobBusy()})

	case "/accounts/import":
		p := str("path")
		if p == "" {
			ps, err := openFilesDialog("选择账号 Excel（.xlsx）", false)
			if err != nil {
				errJSON(w, err)
				return
			}
			if len(ps) == 0 {
				okJSON(w, map[string]any{"canceled": true})
				return
			}
			p = ps[0]
		}
		n, err := importAccounts(p)
		if err != nil {
			errJSON(w, err)
			return
		}
		okJSON(w, map[string]any{"added": n, "file": filepath.Base(p)})

	case "/accounts/verify":
		names := strList("names")
		go verifyAsync(names)
		okJSON(w, map[string]any{"started": true})

	case "/accounts/del":
		n := deleteAccounts(strList("names"))
		okJSON(w, map[string]any{"removed": n})

	case "/accounts/clear":
		n := deleteAccounts(nil)
		okJSON(w, map[string]any{"removed": n})

	case "/pick/file":
		ps, err := openFilesDialog("选择要上传的文件", false)
		if err != nil {
			errJSON(w, err)
			return
		}
		if len(ps) == 0 {
			okJSON(w, map[string]any{"canceled": true})
			return
		}
		st, _ := os.Stat(ps[0])
		var size int64
		if st != nil {
			size = st.Size()
		}
		okJSON(w, map[string]any{"path": ps[0], "name": filepath.Base(ps[0]), "size": size})

	case "/pick/folder":
		p, err := pickFolderDialog("选择 m3u8 保存目录")
		if err != nil {
			errJSON(w, err)
			return
		}
		if p == "" {
			okJSON(w, map[string]any{"canceled": true})
			return
		}
		okJSON(w, map[string]any{"path": p})

	case "/settings":
		s := getSettings()
		if v := str("m3u8Dir"); v != "" {
			s.M3U8Dir = v
		}
		if err := saveSettings(s); err != nil {
			errJSON(w, err)
			return
		}
		okJSON(w, map[string]any{"settings": getSettings()})

	case "/upload/start":
		file := str("path")
		mode := str("mode")
		if mode == "" {
			mode = "random"
		}
		if err := startUpload(file, mode, str("account")); err != nil {
			errJSON(w, err)
			return
		}
		okJSON(w, map[string]any{"started": true})

	case "/upload/cancel":
		cancelUpload()
		okJSON(w, nil)

	case "/open":
		p := str("path")
		if p == "" {
			errJSON(w, fmt.Errorf("路径为空"))
			return
		}
		var err error
		if body["reveal"] == true {
			err = shellReveal(p)
		} else {
			err = shellOpen(p)
		}
		if err != nil {
			errJSON(w, err)
			return
		}
		okJSON(w, nil)

	// ---- 自绘标题栏用的窗口操作 ----
	case "/window/min":
		winMinimize(mainHWND)
		okJSON(w, nil)
	case "/window/max":
		z := winToggleMax(mainHWND)
		okJSON(w, map[string]any{"maximized": z})
	case "/window/close":
		winClose(mainHWND)
		okJSON(w, nil)
	case "/window/drag":
		winStartDrag(mainHWND)
		okJSON(w, nil)
	case "/window/resize":
		if code := resizeCodeOf(str("edge")); code != 0 {
			winStartResize(mainHWND, code)
		}
		okJSON(w, nil)
	case "/window/state":
		okJSON(w, map[string]any{"maximized": isZoomed(mainHWND)})

	default:
		w.WriteHeader(404)
		_, _ = w.Write([]byte("not found"))
	}
}
