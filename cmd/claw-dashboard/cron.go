package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// cronTick 是掃描間隔：每輪檢查有無到點的 job。cron 最小粒度是「分」，30s 足夠且不會漏。
const cronTick = 30 * time.Second

// errAgentBusy＝本行程已有 agent run 在跑。排程器不排隊，下一輪再試（避免堆積）。
var errAgentBusy = errors.New("agent 忙碌中")

// cronScheduler 是 dashboard 內的排程器：每 cronTick 掃 .claw/cron.json，到點的啟用 job 交給
// chatRunner 執行。
//
// 行程模型（先講清楚天花板）：只在 dashboard 跑著時會觸發，【不是 24/7 daemon】。同一份 cron.json
// 之後可讓 bot 端排程器也讀，就能常駐。
type cronScheduler struct {
	path string       // .claw/cron.json
	chat *chatRunner  // 執行器；nil＝未啟用 chat（cron 驅動 agent＝寫能力，沿用同一個閘）
	mu   sync.Mutex   // 保護 base
	base map[string]time.Time // job id → 算下次觸發的基準點
	now  func() time.Time     // 可注入，供測試
}

func newCronScheduler(workspace string, chat *chatRunner) *cronScheduler {
	return &cronScheduler{
		path: cronConfigPath(workspace),
		chat: chat,
		base: map[string]time.Time{},
		now:  time.Now,
	}
}

// run 是排程主迴圈（由呼叫端起 goroutine）。stop 關閉即結束。
func (s *cronScheduler) run(stop <-chan struct{}) {
	t := time.NewTicker(cronTick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.tick()
		}
	}
}

// tick 掃一輪：到點且啟用的 job 逐一執行（序列化——執行器本身也只允許一次一個 run）。
func (s *cronScheduler) tick() {
	jobs, err := readCronJobs(s.path)
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
func (s *cronScheduler) due(j cronJob, now time.Time) bool {
	sched, err := cronParser.Parse(j.Schedule)
	if err != nil {
		return false
	}
	return !sched.Next(s.baseline(j, now)).After(now)
}

// baseline 取算下次觸發的基準點：優先用上次執行時間（持久化，跨重啟仍準）；沒跑過就用【首次觀察到的
// 時刻】——這樣剛新增的 job 不會立刻補跑一次「過去的」排程。
func (s *cronScheduler) baseline(j cronJob, now time.Time) time.Time {
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

// fire 執行一個 job 並回寫結果。忙碌時直接返回且【不動基準】，下一輪自然重試。
func (s *cronScheduler) fire(j cronJob, now time.Time) {
	if s.chat == nil {
		return // 未啟用 chat：不執行（UI 會標示排程器未啟用）
	}
	err := s.chat.runJob(cronSessionID(j.ID), j.Prompt)
	if errors.Is(err, errAgentBusy) {
		return
	}
	s.mu.Lock()
	s.base[j.ID] = now
	s.mu.Unlock()

	status, msg := "ok", ""
	if err != nil {
		status, msg = "error", err.Error()
	}
	if e := setCronResult(s.path, j.ID, status, msg, now.Format(time.RFC3339)); e != nil {
		log.Printf("[cron] 回寫結果失敗：%v", e)
	}
	log.Printf("[cron] %q（%s）→ %s", j.Name, j.Schedule, status)
}

// cronSessionID 讓每個 job 有自己的 session，執行樹可在 /runs/cron-<id> 回放。
func cronSessionID(id string) string { return "cron-" + id }

// runJob 同步跑一輪排程任務：獨立 session、終端 reporter（不打 operator chat 的 SSE 串流）。
// 與 operator chat 共用同一把鎖——本行程一次只跑一個 agent run，避免共用 registry/工具的併發風險。
//
// ponytail: 每次執行前 Reset——排程任務彼此獨立，且避免同一 session 歷史無限長。代價是只留「最近一次」
// 執行樹；要保留每次歷史就得一次一 session（sessions 會無限長），需要時再改。
func (c *chatRunner) runJob(sessionID, prompt string) error {
	if !c.mu.TryLock() {
		return errAgentBusy
	}
	defer c.mu.Unlock()
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, c.workDir)
	sess.Reset()
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})
	return c.eng.Run(context.Background(), sess, engine.NewTerminalReporter())
}
