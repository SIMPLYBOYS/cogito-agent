// Package cron 是排程任務子系統：到點把任務交給 agent 執行。
//
// 【為何獨立成套件】排程器原本住在 dashboard 行程裡，dashboard 一關排程就停。抽出來後 bot
// （cmd/claw，本來就 24/7）也能跑同一份排程，UI 關掉照樣觸發。兩個行程可同時跑排程器——靠
// 跨行程檔案鎖保證同一時刻只有一個真的執行（見 lock_unix.go）。
package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	robfig "github.com/robfig/cron/v3"
)

// Job 是一個排程任務。Schedule 為標準 5-field cron 運算式；Prompt 為到點交給 agent 的任務描述。
// LastRun/LastStatus/LastError 由排程器回寫，供面板顯示。
type Job struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	Prompt     string `json:"prompt"`
	Enabled    bool   `json:"enabled"`
	LastRun    string `json:"last_run,omitempty"`    // RFC3339
	LastStatus string `json:"last_status,omitempty"` // ok / error
	LastError  string `json:"last_error,omitempty"`
}

// parser 是標準 5-field（分 時 日 月 週），不含秒。
var parser = robfig.NewParser(robfig.Minute | robfig.Hour | robfig.Dom | robfig.Month | robfig.Dow)

// ValidateSchedule 檢查 cron 運算式合法（新增/編輯時擋掉壞值，排程器才不會拿到爛表達式）。
func ValidateSchedule(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return fmt.Errorf("排程不可為空")
	}
	if _, err := parser.Parse(expr); err != nil {
		return fmt.Errorf("cron 運算式無效：%v", err)
	}
	return nil
}

// NextRun 算某排程在 from 之後的下次觸發時間（供 UI 顯示）。運算式無效回 ok=false。
func NextRun(schedule string, from time.Time) (time.Time, bool) {
	sched, err := parser.Parse(schedule)
	if err != nil {
		return time.Time{}, false
	}
	return sched.Next(from), true
}

// SessionID 讓每個 job 有自己的 session，執行樹可在 /runs/cron-<id> 回放。
func SessionID(jobID string) string { return "cron-" + jobID }

// ConfigPath 回 job 定義檔路徑（<workspace>/.claw/cron.json）。
func ConfigPath(workspace string) string {
	return filepath.Join(workspace, ".claw", "cron.json")
}

// ReadJobs 讀 cron.json（缺檔＝空清單）。回傳依名稱排序，顯示穩定。
func ReadJobs(path string) ([]Job, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("解析 cron.json 失敗: %w", err)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	return jobs, nil
}

// writeJobs 原子寫回（0600——prompt 可能含敏感內容）。
func writeJobs(path string, jobs []Job) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Add 新增一個 job（預設啟用）。名稱、描述必填；排程須為合法 cron 運算式。
func Add(path, name, schedule, prompt string) error {
	name, schedule, prompt = strings.TrimSpace(name), strings.TrimSpace(schedule), strings.TrimSpace(prompt)
	if name == "" {
		return fmt.Errorf("名稱不可為空")
	}
	if prompt == "" {
		return fmt.Errorf("任務描述不可為空")
	}
	if err := ValidateSchedule(schedule); err != nil {
		return err
	}
	jobs, err := ReadJobs(path)
	if err != nil {
		return err
	}
	jobs = append(jobs, Job{ID: newID(), Name: name, Schedule: schedule, Prompt: prompt, Enabled: true})
	return writeJobs(path, jobs)
}

// Edit 改既有 job 的名稱/排程/描述（不動 LastRun 等執行結果欄位）。
func Edit(path, id, name, schedule, prompt string) error {
	name, schedule, prompt = strings.TrimSpace(name), strings.TrimSpace(schedule), strings.TrimSpace(prompt)
	if name == "" || prompt == "" {
		return fmt.Errorf("名稱與任務描述不可為空")
	}
	if err := ValidateSchedule(schedule); err != nil {
		return err
	}
	return mutate(path, id, func(j *Job) { j.Name, j.Schedule, j.Prompt = name, schedule, prompt })
}

func Remove(path, id string) error {
	jobs, err := ReadJobs(path)
	if err != nil {
		return err
	}
	out := jobs[:0]
	for _, j := range jobs {
		if j.ID != id {
			out = append(out, j)
		}
	}
	return writeJobs(path, out)
}

func Toggle(path, id string) error {
	return mutate(path, id, func(j *Job) { j.Enabled = !j.Enabled })
}

// SetResult 回寫某 job 的執行結果（排程器/立即執行用）。
func SetResult(path, id, status, errMsg, lastRun string) error {
	return mutate(path, id, func(j *Job) {
		j.LastStatus, j.LastError, j.LastRun = status, errMsg, lastRun
	})
}

// mutate 讀-改-寫單一 job（找不到回錯）。集中一處避免各操作各寫一遍讀寫。
func mutate(path, id string, fn func(*Job)) error {
	jobs, err := ReadJobs(path)
	if err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].ID == id {
			fn(&jobs[i])
			return writeJobs(path, jobs)
		}
	}
	return fmt.Errorf("找不到 job %q", id)
}
