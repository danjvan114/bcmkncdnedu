package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

/* ==========================================================================
   账号库
   --------------------------------------------------------------------------
   导入 = 读 Excel → 去重 → 逐个打登录接口验证 → 通过的才算导入成功。
   账号密码只存在本机 <exe>/data/accounts.json（不联网、不上传任何地方）。
   ========================================================================== */

type Account struct {
	Account   string `json:"account"`
	Password  string `json:"password"`
	UID       int64  `json:"uid"`
	Nick      string `json:"nick"`
	Name      string `json:"name"`
	Class     string `json:"class"`
	Token     string `json:"token"`
	Status    string `json:"status"` // pending | ok | fail
	Error     string `json:"error"`
	CheckedAt int64  `json:"checkedAt"`
}

var (
	accMu      sync.Mutex
	accounts   []*Account
	verifyBusy bool
	verifyDone int
	verifyAll  int
)

func accountsFile() string { return filepath.Join(dataDir, "accounts.json") }

func loadAccounts() {
	accMu.Lock()
	defer accMu.Unlock()
	b, err := os.ReadFile(accountsFile())
	if err != nil {
		return
	}
	var list []*Account
	if json.Unmarshal(b, &list) == nil {
		accounts = list
	}
}

func saveAccountsLocked() {
	b, _ := json.MarshalIndent(accounts, "", "  ")
	_ = os.WriteFile(accountsFile(), b, 0o644)
}

// accountView 给前端的视图：不带密码
type accountView struct {
	Account   string `json:"account"`
	UID       int64  `json:"uid"`
	Nick      string `json:"nick"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	CheckedAt int64  `json:"checkedAt"`
}

func accountsView() []accountView {
	accMu.Lock()
	defer accMu.Unlock()
	out := make([]accountView, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, accountView{
			Account: a.Account, UID: a.UID, Nick: a.Nick,
			Status: a.Status, Error: a.Error, CheckedAt: a.CheckedAt,
		})
	}
	return out
}

func accountStats() (total, ok, fail, pending int) {
	accMu.Lock()
	defer accMu.Unlock()
	for _, a := range accounts {
		total++
		switch a.Status {
		case "ok":
			ok++
		case "fail":
			fail++
		default:
			pending++
		}
	}
	return
}

// accountNamesOK 可用账号的账号名快照（随机模式的候选池）
func accountNamesOK() []string {
	accMu.Lock()
	defer accMu.Unlock()
	var out []string
	for _, a := range accounts {
		if a.Status == "ok" {
			out = append(out, a.Account)
		}
	}
	return out
}

// accountToken 取账号 token（没有就重登一次），顺手把最新信息写回账号库。
// 返回的 token 只用于「取上传凭证」，不会落到前端。
func accountToken(name string) (string, int64, error) {
	accMu.Lock()
	var a *Account
	for _, x := range accounts {
		if x.Account == name {
			a = x
			break
		}
	}
	if a == nil {
		accMu.Unlock()
		return "", 0, errors.New("账号不存在：" + name)
	}
	if a.Token != "" {
		tok, uid := a.Token, a.UID
		accMu.Unlock()
		return tok, uid, nil
	}
	pwd := a.Password
	accMu.Unlock()

	u, err := cmLogin(name, pwd)
	if err != nil {
		accMu.Lock()
		a.Status = "fail"
		a.Error = err.Error()
		a.Token = ""
		a.CheckedAt = time.Now().Unix()
		saveAccountsLocked()
		accMu.Unlock()
		return "", 0, err
	}

	accMu.Lock()
	a.Token = u.Token
	a.UID = u.UID
	a.Nick = u.Nick
	a.Status = "ok"
	a.Error = ""
	a.CheckedAt = time.Now().Unix()
	saveAccountsLocked()
	accMu.Unlock()
	return u.Token, u.UID, nil
}

// ---------------------------------------------------------------------------
// 导入
// ---------------------------------------------------------------------------

// importAccounts 读 Excel，把没见过的账号加进来（状态 pending），返回新增数量。
// 真正的验证是异步跑的，前端轮询进度。
func importAccounts(file string) (int, error) {
	rows, err := readXLSX(file)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, errors.New("没读到账号 —— 表头需要有「账号」和「密码」两列")
	}

	accMu.Lock()
	have := map[string]bool{}
	for _, a := range accounts {
		have[a.Account] = true
	}
	added := 0
	for _, r := range rows {
		if r.Account == "" || have[r.Account] {
			continue
		}
		have[r.Account] = true
		accounts = append(accounts, &Account{
			Account:  r.Account,
			Password: r.Password,
			Name:     r.Name,
			Class:    r.Class,
			Status:   "pending",
		})
		added++
	}
	saveAccountsLocked()
	accMu.Unlock()

	if added > 0 {
		go verifyAsync(nil) // 全部待验证的
	}
	return added, nil
}

// verifyAsync 并发打登录接口。names 为空 = 所有非 ok 的账号。
func verifyAsync(names []string) {
	accMu.Lock()
	if verifyBusy {
		accMu.Unlock()
		return
	}
	var todo []*Account
	if len(names) == 0 {
		for _, a := range accounts {
			if a.Status != "ok" {
				todo = append(todo, a)
			}
		}
	} else {
		want := map[string]bool{}
		for _, n := range names {
			want[n] = true
		}
		for _, a := range accounts {
			if want[a.Account] {
				todo = append(todo, a)
			}
		}
	}
	for _, a := range todo {
		a.Status = "pending"
		a.Error = ""
	}
	verifyBusy = true
	verifyDone = 0
	verifyAll = len(todo)
	accMu.Unlock()

	if len(todo) == 0 {
		accMu.Lock()
		verifyBusy = false
		accMu.Unlock()
		return
	}

	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, a := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(a *Account) {
			defer wg.Done()
			defer func() { <-sem }()
			time.Sleep(120 * time.Millisecond) // 别把接口打太快
			u, err := cmLogin(a.Account, a.Password)

			accMu.Lock()
			if err != nil {
				a.Status = "fail"
				a.Error = err.Error()
				a.Token = ""
			} else {
				a.Status = "ok"
				a.Error = ""
				a.UID = u.UID
				a.Nick = u.Nick
				a.Token = u.Token
			}
			a.CheckedAt = time.Now().Unix()
			verifyDone++
			saveAccountsLocked()
			accMu.Unlock()
		}(a)
	}
	wg.Wait()

	accMu.Lock()
	verifyBusy = false
	accMu.Unlock()
}

// deleteAccounts 删账号（names 为空 = 清空）
func deleteAccounts(names []string) int {
	accMu.Lock()
	defer accMu.Unlock()
	if len(names) == 0 {
		n := len(accounts)
		accounts = nil
		saveAccountsLocked()
		return n
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	kept := accounts[:0]
	removed := 0
	for _, a := range accounts {
		if want[a.Account] {
			removed++
			continue
		}
		kept = append(kept, a)
	}
	accounts = kept
	saveAccountsLocked()
	return removed
}

func verifyProgress() (busy bool, done, total int) {
	accMu.Lock()
	defer accMu.Unlock()
	return verifyBusy, verifyDone, verifyAll
}
