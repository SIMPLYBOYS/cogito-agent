package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/robfig/cron/v3"
)

// cronJob 是一個排程任務。Schedule 為標準 5-field cron 運算式；Prompt 為到點交給 agent 的任務描述。
// LastRun/LastStatus/LastError 由排程器回寫，供面板顯示。
type cronJob struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	Prompt     string `json:"prompt"`
	Enabled    bool   `json:"enabled"`
	LastRun    string `json:"last_run,omitempty"`    // RFC3339
	LastStatus string `json:"last_status,omitempty"` // ok / error / running
	LastError  string `json:"last_error,omitempty"`
}

// cronParser 與排程器共用的解析器：標準 5-field（分 時 日 月 週），不含秒。ParseStandard 會驗證運算式。
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// validateSchedule 檢查 cron 運算式合法（面板新增/編輯時擋掉壞值，排程器才不會拿到爛表達式）。
func validateSchedule(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return fmt.Errorf("排程不可為空")
	}
	if _, err := cronParser.Parse(expr); err != nil {
		return fmt.Errorf("cron 運算式無效：%v", err)
	}
	return nil
}

func cronConfigPath(workspace string) string {
	return filepath.Join(workspace, ".claw", "cron.json")
}

// readCronJobs 讀 cron.json（缺檔＝空清單）。回傳依名稱排序，顯示穩定。
func readCronJobs(path string) ([]cronJob, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []cronJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("解析 cron.json 失敗: %w", err)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	return jobs, nil
}

// writeCronJobs 原子寫回（0600——prompt 可能含敏感內容）。
func writeCronJobs(path string, jobs []cronJob) error {
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

func newCronID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// addCronJob 新增一個 job（預設啟用）。名稱與排程必填；排程須為合法 cron 運算式。
func addCronJob(path, name, schedule, prompt string) error {
	name, schedule, prompt = strings.TrimSpace(name), strings.TrimSpace(schedule), strings.TrimSpace(prompt)
	if name == "" {
		return fmt.Errorf("名稱不可為空")
	}
	if prompt == "" {
		return fmt.Errorf("任務描述不可為空")
	}
	if err := validateSchedule(schedule); err != nil {
		return err
	}
	jobs, err := readCronJobs(path)
	if err != nil {
		return err
	}
	jobs = append(jobs, cronJob{ID: newCronID(), Name: name, Schedule: schedule, Prompt: prompt, Enabled: true})
	return writeCronJobs(path, jobs)
}

// editCronJob 改既有 job 的名稱/排程/描述（不動 LastRun 等執行結果欄位）。
func editCronJob(path, id, name, schedule, prompt string) error {
	name, schedule, prompt = strings.TrimSpace(name), strings.TrimSpace(schedule), strings.TrimSpace(prompt)
	if name == "" || prompt == "" {
		return fmt.Errorf("名稱與任務描述不可為空")
	}
	if err := validateSchedule(schedule); err != nil {
		return err
	}
	return mutateCronJob(path, id, func(j *cronJob) { j.Name, j.Schedule, j.Prompt = name, schedule, prompt })
}

func removeCronJob(path, id string) error {
	jobs, err := readCronJobs(path)
	if err != nil {
		return err
	}
	out := jobs[:0]
	for _, j := range jobs {
		if j.ID != id {
			out = append(out, j)
		}
	}
	return writeCronJobs(path, out)
}

func toggleCronJob(path, id string) error {
	return mutateCronJob(path, id, func(j *cronJob) { j.Enabled = !j.Enabled })
}

// setCronResult 回寫某 job 的執行結果（排程器/立即執行用）。找不到 id 就當 no-op（job 可能已被刪）。
func setCronResult(path, id, status, errMsg, lastRun string) error {
	return mutateCronJob(path, id, func(j *cronJob) {
		j.LastStatus, j.LastError, j.LastRun = status, errMsg, lastRun
	})
}

// mutateCronJob 讀-改-寫單一 job（找不到回錯）。集中一處避免各操作各寫一遍讀寫。
func mutateCronJob(path, id string, fn func(*cronJob)) error {
	jobs, err := readCronJobs(path)
	if err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].ID == id {
			fn(&jobs[i])
			return writeCronJobs(path, jobs)
		}
	}
	return fmt.Errorf("找不到 job %q", id)
}
