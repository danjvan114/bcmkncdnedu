package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

/* ==========================================================================
   编程猫对象存储（底层七牛 Kodo）—— 上传三步
   --------------------------------------------------------------------------
     1) POST https://api.codemao.cn/tiger/v3/web/accounts/login   {pid,identity,password}
        → {auth:{token}, user_info:{id,nickname}}
     2) GET  https://open-service.codemao.cn/cdn/qi-niu/tokens/uploading
             ?filePaths=<uid>/<keyName>&projectName=neko      （带 Authorization: token）
        → {bucket_url, tokens:[{token, file_path}]}
     3) POST https://upload.qiniup.com/  multipart(key,token,file)  → {key}
        直链 = bucket_url + key

   实测结论（2026-10-30，拿真账号打过）：
   - .ts / .m4s / .mp4 都能直接传，回源 Content-Type 分别是
     video/MP2T / video/iso.segment / video/mp4 → HLS 切片不用套壳。
   - 桶 insertOnly=1，同一个 key 传第二次会报 614 file exists → key 必须带随机段。
   - 单对象 50MB 上限，所以切片目标定在 30~40MB。
   ========================================================================== */

const uaString = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

const (
	loginURL  = "https://api.codemao.cn/tiger/v3/web/accounts/login"
	tokenURL  = "https://open-service.codemao.cn/cdn/qi-niu/tokens/uploading"
	qiniuURL  = "https://upload.qiniup.com/"
	pidString = "65edCTyg"
)

var apiClient = &http.Client{
	Timeout: 0, // 上传大切片不能有整体超时
	Transport: &http.Transport{
		Proxy:               nil, // 别走系统代理，直连
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
		IdleConnTimeout:     60 * time.Second,
		ForceAttemptHTTP2:   true,
	},
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// 登录
// ---------------------------------------------------------------------------

type cmUser struct {
	Token string
	UID   int64
	Nick  string
}

func cmLogin(account, password string) (*cmUser, error) {
	body, _ := json.Marshal(map[string]string{
		"pid":      pidString,
		"identity": account,
		"password": password,
	})
	req, err := http.NewRequest("POST", loginURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", uaString)

	res, err := apiClient.Do(req)
	if err != nil {
		return nil, errors.New("网络错误：" + err.Error())
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)

	var r struct {
		Auth struct {
			Token string `json:"token"`
		} `json:"auth"`
		UserInfo struct {
			ID       int64  `json:"id"`
			Nickname string `json:"nickname"`
		} `json:"user_info"`
		ErrorCode int    `json:"error_code"`
		ErrorMsg  string `json:"error_message"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.Auth.Token == "" {
		msg := r.ErrorMsg
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return nil, fmt.Errorf("登录失败（HTTP %d）：%s", res.StatusCode, clipStr(msg, 120))
	}
	return &cmUser{Token: r.Auth.Token, UID: r.UserInfo.ID, Nick: r.UserInfo.Nickname}, nil
}

// ---------------------------------------------------------------------------
// 上传凭证
// ---------------------------------------------------------------------------

type uploadToken struct {
	Token  string
	Key    string
	Bucket string
}

func cmUploadToken(token string, uid int64, keyName string) (*uploadToken, error) {
	u := fmt.Sprintf("%s?filePaths=%s&projectName=neko",
		tokenURL, url.QueryEscape(fmt.Sprintf("%d/%s", uid, keyName)))
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", token)
	req.Header.Set("User-Agent", uaString)

	res, err := apiClient.Do(req)
	if err != nil {
		return nil, errors.New("网络错误：" + err.Error())
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)

	var r struct {
		Bucket string `json:"bucket_url"`
		Tokens []struct {
			Token    string `json:"token"`
			FilePath string `json:"file_path"`
		} `json:"tokens"`
	}
	_ = json.Unmarshal(raw, &r)
	if len(r.Tokens) == 0 || r.Tokens[0].Token == "" {
		return nil, fmt.Errorf("取上传凭证失败（HTTP %d）：%s", res.StatusCode, clipStr(string(raw), 120))
	}
	return &uploadToken{
		Token:  r.Tokens[0].Token,
		Key:    r.Tokens[0].FilePath,
		Bucket: r.Bucket,
	}, nil
}

// ---------------------------------------------------------------------------
// 传七牛
// ---------------------------------------------------------------------------

// progressReader 把已读字节数回吐给进度回调
type progressReader struct {
	r   io.Reader
	n   int64
	max int64
	cb  func(int64, int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.n += int64(n)
		if p.cb != nil {
			p.cb(p.n, p.max)
		}
	}
	return n, err
}

// qiniuPut 手写 multipart：能算出精确 Content-Length（不用 chunked，服务端更稳），
// 文件流式读，不整块进内存。
func qiniuPut(tk *uploadToken, localPath, contentType string, size int64, onProgress func(int64, int64)) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	boundary := "----PickcatEdu" + randHex(12)
	head := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"key\"\r\n\r\n" + tk.Key + "\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"token\"\r\n\r\n" + tk.Token + "\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"file\"; filename=\"blob\"\r\n" +
		"Content-Type: " + contentType + "\r\n\r\n"
	tail := "\r\n--" + boundary + "--\r\n"

	pr := &progressReader{r: f, max: size, cb: onProgress}
	body := io.MultiReader(strings.NewReader(head), pr, strings.NewReader(tail))

	req, err := http.NewRequest("POST", qiniuURL, body)
	if err != nil {
		return "", err
	}
	req.ContentLength = int64(len(head)) + size + int64(len(tail))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", uaString)

	res, err := apiClient.Do(req)
	if err != nil {
		return "", errors.New("上传中断：" + err.Error())
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return "", errors.New(qiniuErr(raw, res.StatusCode))
	}
	var r struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.Key == "" {
		r.Key = tk.Key
	}
	return r.Key, nil
}

func qiniuErr(body []byte, code int) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := e.Error
	if msg == "" {
		msg = clipStr(string(body), 160)
	}
	switch {
	case strings.Contains(msg, "limited mimeType"):
		return "文件类型被对象存储拒绝（内容不在白名单里）"
	case strings.Contains(msg, "file exists"):
		return "云端已存在同名对象，换个名字再试"
	case strings.Contains(msg, "fsizeLimit"), strings.Contains(msg, "too large"):
		return "文件超过 50MB 上限，切片再小一点"
	case strings.Contains(msg, "expired"):
		return "上传凭证已过期，请重试"
	}
	return fmt.Sprintf("上传失败（HTTP %d）：%s", code, msg)
}

func clipStr(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
