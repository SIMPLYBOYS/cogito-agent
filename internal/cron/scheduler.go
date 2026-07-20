package cron

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// TickInterval 是掃描間隔：每輪檢查有無到點的 job。cron 最小粒度是「分」，30s 足夠且不會漏。
const TickInterval = 30 * time.Second

// ErrBusy＝執行器正忙（該行程已有 agent run 在跑）。排程器不排隊，下一輪再試（避免堆積）。
var ErrBusy = errors.New("agent 忙碌中")

// TZKey 設定排程解讀用的時區（IANA 名稱，如 Asia/Taipei）。
//
// 【為何需要】排程是牆上時鐘樣式，得先知道是「誰的牆」。雲端機器預設多為 UTC，若依賴伺服器本地
// 時區，「0 9 * * *」會變成台北下午五點。設了這個就與部署環境的 TZ 脫鉤。
const TZKey = "CRON_TZ"

// Location 回排程用時區；空＝伺服器本地時區。第二個回傳值是設定有問題時的說明（供 UI 提示），
// 空＝正常。無效時退回本地時區而非讓排程整個停擺。
func Location() (*time.Location, string) {
	tz := strings.TrimSpace(os.Getenv(TZKey))
	if tz == "" {
		return time.Local, ""
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Local, fmt.Sprintf("時區 %q 無效（%v）——暫用伺服器本地時區", tz, err)
	}
	return loc, ""
}

// Runner 執行一個排程任務並回傳 agent 的最終回覆（供推播摘要）。
//
// dashboard 與 bot 各自實作：兩邊都有組好的 agent，但組法不同（dashboard 是 operator chat 那套，
// bot 是 per-session factory）。忙碌時回 ErrBusy，排程器就跳過本輪、下輪再試。
type Runner interface {
	RunJob(sessionID, prompt string) (reply string, err error)
}

// Scheduler 每 TickInterval 掃 cron.json，到點的啟用 job 交給 Runner 執行。
//
// 【多行程】dashboard 與 bot 可同時各跑一個 Scheduler（bot 常駐＝關掉 UI 也照跑）。同一時刻只有
// 一個會真的執行——靠 lockPath 的跨行程檔案鎖仲裁，搶不到就跳過本輪。
type Scheduler struct {
	path     string // cron.json
	lockPath string
	runner   Runner
	label    string // 記 log 用，分辨是哪個行程跑的

	mu   sync.Mutex
	base map[string]time.Time // job id → 算下次觸發的基準點
	now  func() time.Time     // 可注入，供測試

	// running 是正在執行的 job id（空＝閒置）。cron 刻意不打 operator chat 的 SSE 串流
	//（會污染對話），改用這個讓 cron 頁顯示「執行中」。
	running string
}

// New 建一個排程器。workspace 決定 cron.json 位置；label 只用於 log。
func New(workspace string, runner Runner, label string) *Scheduler {
	path := ConfigPath(workspace)
	return &Scheduler{
		path:     path,
		lockPath: path + ".lock",
		runner:   runner,
		label:    label,
		base:     map[string]time.Time{},
		now:      time.Now,
	}
}

// Run 是排程主迴圈（由呼叫端起 goroutine）。stop 關閉即結束。
func (s *Scheduler) Run(stop <-chan struct{}) {
	log.Printf("[cron] 排程器啟動（%s，每 %s 檢查一次）", s.label, TickInterval)
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.Tick()
		}
	}
}

// Tick 掃一輪：到點且啟用的 job 逐一執行。
//
// 開頭先搶跨行程鎖：另一個行程（bot／dashboard）正在跑排程就直接返回，不重複執行。鎖持有整輪，
// 因此對方在我們跑任務期間會跳過——這正是要的，任務已經有人在跑。
func (s *Scheduler) Tick() {
	unlock, ok := acquireLock(s.lockPath)
	if !ok {
		return // 另一個行程正在跑這一輪
	}
	defer unlock()

	jobs, err := ReadJobs(s.path)
	if err != nil {
		log.Printf("[cron] 讀 cron.json 失敗：%v", err)
		return
	}
	now := s.now()
	for _, j := range jobs {
		if j.Enabled && s.due(j, now) {
			s.fire(j, now)
		}
	}
}

// due 判斷 job 是否到點：下次觸發時間 <= 現在。壞運算式一律不跑（新增時已驗，這裡是保險）。
// 基準點先轉到設定時區再算——robfig 的 Next 依傳入時間的 Location 推算，故時區在此生效。
func (s *Scheduler) due(j Job, now time.Time) bool {
	loc, _ := Location()
	next, ok := NextRun(j.Schedule, s.baseline(j, now).In(loc))
	return ok && !next.After(now)
}

// baseline 取算下次觸發的基準點：優先用上次執行時間（持久化，跨重啟仍準）；沒跑過就用【首次觀察到
// 的時刻】——這樣剛新增的 job 不會立刻補跑一次「過去的」排程。
func (s *Scheduler) baseline(j Job, now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.base[j.ID]; ok {
		return b
	}
	b := now
	if j.LastRun != "" {
		if t, err := time.Parse(time.RFC3339, j.LastRun); err == nil {
			b = t
		}
	}
	s.base[j.ID] = b
	return b
}

// Fire 立刻執行一個 job（面板的「立即執行」用，不等排程）。
func (s *Scheduler) Fire(j Job) { s.fire(j, s.now()) }

// fire 執行一個 job 並回寫結果。忙碌時直接返回且【不動基準】，下一輪自然重試。
func (s *Scheduler) fire(j Job, now time.Time) {
	if !s.tryMarkRunning(j.ID) {
		return // 已有 job 在跑：不排隊，下輪再試
	}
	defer s.clearRunning()

	started := s.now()
	reply, err := s.runner.RunJob(SessionID(j.ID), j.Prompt)
	if errors.Is(err, ErrBusy) {
		return // 執行器被別的 run 佔用：同上，下輪再試
	}
	s.mu.Lock()
	s.base[j.ID] = now
	s.mu.Unlock()

	status, msg := "ok", ""
	if err != nil {
		status, msg = "error", err.Error()
	}
	if e := SetResult(s.path, j.ID, status, msg, now.Format(time.RFC3339)); e != nil {
		log.Printf("[cron] 回寫結果失敗：%v", e)
	}
	log.Printf("[cron] %q（%s）→ %s（%s）", j.Name, j.Schedule, status, s.label)

	// 推播結果。失敗只記 log——通知掛掉不該影響排程本身。
	if target := NotifyTarget(); ShouldNotify(target, status) {
		notice := buildNotice(j, status, msg, reply, s.label, s.now().Sub(started))
		if e := SendAll(target, notice); e != nil {
			log.Printf("[cron] 推播失敗：%v", e)
		}
	}
}

// tryMarkRunning 標記本 job 開跑；已有其他 job 在跑就回 false（不覆寫別人的狀態——否則被執行器
// 擋掉的那次 fire 會把真正在跑的那個標記清掉）。
func (s *Scheduler) tryMarkRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running != "" {
		return false
	}
	s.running = id
	return true
}

func (s *Scheduler) clearRunning() {
	s.mu.Lock()
	s.running = ""
	s.mu.Unlock()
}

// RunningID 回目前正在執行的 job id（空＝閒置）。
func (s *Scheduler) RunningID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}
