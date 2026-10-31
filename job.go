package main

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

/* ==========================================================================
   上传任务
   --------------------------------------------------------------------------
   流程：探测 → 切片（>40MB 才切）→ 多线程上传（每片换账号）→ 拼 m3u8 存本地
   进度和速度都从「已上传字节数」算，前端 400ms 轮询一次。
   ========================================================================== */

const (
	splitThreshold = 40 << 20 // 超过 40MB 才切片
	uploadThreads  = 4        // 同时传几片
	maxRetry       = 2        // 单片失败换账号重试次数
)

type segState struct {
	Idx     int     `json:"idx"`
	Name    string  `json:"name"`
	Path    string  `json:"-"`
	Size    int64   `json:"size"`
	Done    int64   `json:"done"`
	Account string  `json:"account"`
	Status  string  `json:"status"` // pending | uploading | done | fail
	Err     string  `json:"err"`
	URL     string  `json:"url"`
	Dur     float64 `json:"dur"`
}

type jobState struct {
	ID       string `json:"id"`
	File     string `json:"file"`
	FileName string `json:"fileName"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"` // random | account
	UseAcc   string `json:"useAcc"`

	Stage   string `json:"stage"` // idle | probe | split | upload | write | done | error | canceled
	Message string `json:"message"`

	Segs  []*segState `json:"segs"`
	Total int64       `json:"total"`
	Done  int64       `json:"done"`
	Speed float64     `json:"speed"`

	M3U8Path string  `json:"m3u8Path"`
	Error    string  `json:"error"`
	Started  int64   `json:"started"`
	Elapsed  float64 `json:"elapsed"`
	Accounts int     `json:"accounts"`
}

var (
	jobMu     sync.Mutex
	curJob    *jobState
	jobCancel bool
	jobBusy   bool
)

func currentJob() *jobState {
	jobMu.Lock()
	defer jobMu.Unlock()
	return curJob
}

func jobSnapshot() any {
	jobMu.Lock()
	defer jobMu.Unlock()
	if curJob == nil {
		return nil
	}
	// 复制一份，别让前端读到正在改的切片
	cp := *curJob
	cp.Segs = make([]*segState, len(curJob.Segs))
	for i, s := range curJob.Segs {
		sc := *s
		cp.Segs[i] = &sc
	}
	if cp.Started > 0 && (cp.Stage == "done" || cp.Stage == "error" || cp.Stage == "canceled") {
		cp.Elapsed = float64(time.Now().UnixMilli()-cp.Started) / 1000
	} else if cp.Started > 0 {
		cp.Elapsed = float64(time.Now().UnixMilli()-cp.Started) / 1000
	}
	return &cp
}

func isJobBusy() bool {
	jobMu.Lock()
	defer jobMu.Unlock()
	return jobBusy
}

func setStage(stage, msg string) {
	jobMu.Lock()
	if curJob != nil {
		curJob.Stage = stage
		if msg != "" {
			curJob.Message = msg
		}
	}
	jobMu.Unlock()
}

func failJob(err error) {
	stopSpeedMeter()
	jobMu.Lock()
	if curJob != nil {
		curJob.Stage = "error"
		curJob.Error = err.Error()
		curJob.Message = err.Error()
		curJob.Speed = 0
	}
	jobBusy = false
	jobMu.Unlock()
}

// startUpload 起一个上传任务（立刻返回，前端轮询进度）
func startUpload(file, mode, useAcc string) error {
	if isJobBusy() {
		return errors.New("已经有一个上传任务在跑了")
	}
	st, err := os.Stat(file)
	if err != nil || st.IsDir() {
		return errors.New("文件不存在")
	}

	names := accountNamesOK()
	if len(names) == 0 {
		return errors.New("还没有可用账号，先去首页导入并验证账号")
	}
	if mode == "account" {
		if useAcc == "" {
			return errors.New("指定账号模式需要先选一个账号")
		}
		names = []string{useAcc}
	}

	jobMu.Lock()
	curJob = &jobState{
		ID:       randHex(5),
		File:     file,
		FileName: filepath.Base(file),
		Size:     st.Size(),
		Mode:     mode,
		UseAcc:   useAcc,
		Stage:    "probe",
		Message:  "准备中…",
		Started:  time.Now().UnixMilli(),
		Accounts: len(names),
	}
	jobBusy = true
	jobCancel = false
	jobMu.Unlock()

	go runJob(curJob.ID, names)
	return nil
}

func cancelUpload() {
	jobMu.Lock()
	if jobBusy {
		jobCancel = true
	}
	jobMu.Unlock()
}

func canceled() bool {
	jobMu.Lock()
	defer jobMu.Unlock()
	return jobCancel
}

// ---------------------------------------------------------------------------
// 主流程
// ---------------------------------------------------------------------------

func runJob(jobID string, accNames []string) {
	jobMu.Lock()
	job := curJob
	jobMu.Unlock()
	if job == nil {
		return
	}

	work := filepath.Join(tmpDir(), "job-"+jobID)
	defer os.RemoveAll(work)

	// ---- 1. 探测 ----
	setStage("probe", "读取媒体信息…")
	info, probeErr := probeMedia(job.File)
	if canceled() {
		finishCanceled()
		return
	}

	var segs []*segState
	var directDur float64

	needSplit := job.Size > splitThreshold
	isMedia := probeErr == nil && (info.HasVideo || info.HasAudio)

	if isMedia {
		segSeconds := 10.0
		if !needSplit {
			segSeconds = 3600 // 小文件也走 HLS，只切一片，保证 m3u8 一定能播
		} else if info.Duration > 0 {
			bps := float64(job.Size) / info.Duration
			if bps > 0 {
				t := float64(segTargetBytes) / bps
				segSeconds = math.Max(2, math.Min(10, t))
			}
		}

		setStage("split", fmt.Sprintf("ffmpeg 切片中（每片约 %d 秒）…", int(segSeconds+0.5)))
		list, err := splitHLS(job.File, work, segSeconds, false, nil)
		if err == nil && maxSegSize(list) > segMaxBytes && info.HasVideo {
			// copy 切出来的段太大 → 强制关键帧重编码再切一次
			setStage("split", "片段太大，重新转码切片（会慢一些）…")
			_ = os.RemoveAll(work)
			_ = os.MkdirAll(work, 0o755)
			list, err = splitHLS(job.File, work, segSeconds, true, nil)
		}
		if err != nil {
			if job.Size <= segMaxBytes {
				// 切不动就整份传，m3u8 里只放一个直链
				segs = []*segState{{
					Idx: 0, Name: filepath.Base(job.File), Size: job.Size, Path: job.File,
					Status: "pending", Dur: info.Duration,
				}}
				directDur = info.Duration
			} else {
				failJob(fmt.Errorf("%v（文件又超过 40MB，没法整份上传）", err))
				return
			}
		} else {
			segs = buildSegs(list)
		}
	} else {
		// 不是音视频（pdf / zip / 图片…）→ 整份传
		if job.Size > segMaxBytes {
			failJob(errors.New("这个文件不是音视频，无法切片；超过 45MB 传不了"))
			return
		}
		segs = []*segState{{
			Idx: 0, Name: filepath.Base(job.File), Size: job.Size, Path: job.File, Status: "pending",
		}}
	}

	if canceled() {
		finishCanceled()
		return
	}

	var total int64
	for _, s := range segs {
		total += s.Size
	}
	jobMu.Lock()
	job.Segs = segs
	job.Total = total
	job.Done = 0
	jobMu.Unlock()

	// ---- 2. 上传 ----
	setStage("upload", fmt.Sprintf("上传 %d 个片段（%d 线程）…", len(segs), uploadThreads))
	startSpeedMeter()

	pool := append([]string{}, accNames...)
	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })

	idxCh := make(chan int)
	var wg sync.WaitGroup
	threads := uploadThreads
	if len(segs) < threads {
		threads = len(segs)
	}
	for w := 0; w < threads; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for idx := range idxCh {
				if canceled() {
					continue
				}
				uploadSeg(job, segs[idx], pool, idx, work)
			}
		}(w)
	}
	for i := range segs {
		idxCh <- i
	}
	close(idxCh)
	wg.Wait()
	stopSpeedMeter()

	if canceled() {
		finishCanceled()
		return
	}

	// 有任何一片失败就算失败
	var failed []string
	for _, s := range segs {
		if s.Status != "done" {
			failed = append(failed, fmt.Sprintf("#%d %s", s.Idx+1, s.Err))
		}
	}
	if len(failed) > 0 {
		failJob(errors.New("有片段上传失败：" + strings.Join(failed, "；")))
		return
	}

	// ---- 3. 写 m3u8 ----
	setStage("write", "拼接 m3u8…")
	out, err := writeM3U8(job, segs, directDur)
	if err != nil {
		failJob(err)
		return
	}

	jobMu.Lock()
	job.Stage = "done"
	job.Message = "上传完成"
	job.M3U8Path = out
	job.Done = job.Total
	job.Speed = 0
	jobBusy = false
	jobMu.Unlock()
}

// buildSegs 把 ffmpeg 切出来的片段改成随机名字，再转成任务里的片段。
// 名字跟顺序完全无关；同一次任务里万一撞名就换一个再试。
func buildSegs(list []hlsSegment) []*segState {
	used := map[string]bool{}
	out := make([]*segState, 0, len(list))
	for i, s := range list {
		name, path := s.Name, s.Path
		for {
			name = randSegName()
			if !used[name] {
				break
			}
		}
		used[name] = true
		if np := filepath.Join(filepath.Dir(path), name); os.Rename(path, np) == nil {
			path = np
		}
		out = append(out, &segState{
			Idx: i, Name: name, Path: path, Size: s.Size,
			Status: "pending", Dur: s.Dur,
		})
	}
	return out
}

func finishCanceled() {
	stopSpeedMeter()
	jobMu.Lock()
	if curJob != nil {
		curJob.Stage = "canceled"
		curJob.Message = "已取消"
		curJob.Speed = 0
	}
	jobBusy = false
	jobMu.Unlock()
}

// uploadSeg 传一片：取 token → 取上传凭证 → PUT。失败换账号重试。
func uploadSeg(job *jobState, seg *segState, pool []string, idx int, work string) {
	jobMu.Lock()
	seg.Status = "uploading"
	jobMu.Unlock()

	acc := pool[idx%len(pool)]
	for attempt := 0; attempt <= maxRetry; attempt++ {
		if attempt > 0 {
			acc = pool[(idx+attempt)%len(pool)]
			// 重试要把这一片已经算进去的字节吐回来，否则总进度会超过 100%
			jobMu.Lock()
			job.Done -= seg.Done
			if job.Done < 0 {
				job.Done = 0
			}
			jobMu.Unlock()
		}
		jobMu.Lock()
		seg.Account = acc
		seg.Err = ""
		seg.Done = 0
		jobMu.Unlock()

		url, err := uploadOne(job, seg, acc)
		if err == nil {
			jobMu.Lock()
			seg.URL = url
			seg.Status = "done"
			seg.Done = seg.Size
			jobMu.Unlock()
			return
		}
		jobMu.Lock()
		seg.Err = err.Error()
		jobMu.Unlock()
		time.Sleep(300 * time.Millisecond)
	}
	jobMu.Lock()
	seg.Status = "fail"
	jobMu.Unlock()
}

func uploadOne(job *jobState, seg *segState, acc string) (string, error) {
	token, uid, err := accountToken(acc)
	if err != nil {
		return "", err
	}
	// 云端 key：user-files/<随机名>，服务端只加固定的 922/ 前缀，
	// 最终就是 922/user-files/xxxx.ts —— 跟站内图片同一层，不建自己的目录
	keyName := "user-files/" + seg.Name
	tk, err := cmUploadToken(token, uid, keyName)
	if err != nil {
		return "", err
	}

	contentType := "application/octet-stream"
	low := strings.ToLower(seg.Name)
	if strings.HasSuffix(low, ".ts") {
		contentType = "video/mp2t"
	}

	key, err := qiniuPut(tk, seg.Path, contentType, seg.Size, func(got, max int64) {
		jobMu.Lock()
		delta := got - seg.Done
		if delta > 0 {
			seg.Done = got
			job.Done += delta
		}
		jobMu.Unlock()
	})
	if err != nil {
		return "", err
	}
	return strings.TrimRight(tk.Bucket, "/") + "/" + key, nil
}

// ---------------------------------------------------------------------------
// 速度计
// ---------------------------------------------------------------------------

var (
	speedStop chan struct{}
	speedMu   sync.Mutex
)

func startSpeedMeter() {
	speedMu.Lock()
	defer speedMu.Unlock()
	stop := make(chan struct{})
	speedStop = stop
	go func() {
		tk := time.NewTicker(400 * time.Millisecond)
		defer tk.Stop()
		lastDone := int64(0)
		lastAt := time.Now()
		speed := 0.0
		for {
			select {
			case <-stop:
				return
			case now := <-tk.C:
				jobMu.Lock()
				done := int64(0)
				if curJob != nil {
					done = curJob.Done
				}
				jobMu.Unlock()
				dt := now.Sub(lastAt).Seconds()
				if dt > 0 {
					inst := float64(done-lastDone) / dt
					if inst < 0 {
						inst = 0
					}
					speed = speed*0.6 + inst*0.4
				}
				lastDone, lastAt = done, now
				jobMu.Lock()
				if curJob != nil && jobBusy {
					curJob.Speed = speed
				}
				jobMu.Unlock()
			}
		}
	}()
}

func stopSpeedMeter() {
	speedMu.Lock()
	defer speedMu.Unlock()
	if speedStop != nil {
		close(speedStop)
		speedStop = nil
	}
}

// ---------------------------------------------------------------------------
// m3u8
// ---------------------------------------------------------------------------

func writeM3U8(job *jobState, segs []*segState, directDur float64) (string, error) {
	dir := getSettings().M3U8Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("m3u8 保存目录不可用：%v", err)
	}

	maxDur := 0.0
	for _, s := range segs {
		if s.Dur > maxDur {
			maxDur = s.Dur
		}
	}
	if maxDur < 1 {
		maxDur = 10
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	b.WriteString("#EXT-X-TARGETDURATION:" + strconv.Itoa(int(math.Ceil(maxDur))) + "\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	for _, s := range segs {
		d := s.Dur
		if d <= 0 {
			d = directDur
		}
		if d <= 0 {
			d = 0.5
		}
		b.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n%s\n", d, s.URL))
	}
	b.WriteString("#EXT-X-ENDLIST\n")

	base := strings.TrimSuffix(job.FileName, filepath.Ext(job.FileName)) + ".m3u8"
	out := uniquePath(dir, base)
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("写 m3u8 失败：%v", err)
	}
	return out, nil
}
