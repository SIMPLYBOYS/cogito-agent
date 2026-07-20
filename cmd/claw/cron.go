package main

import (
	"context"
	"sync"

	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/cron"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// botCronRunner 讓【常駐的 bot】也能執行排程任務——這才是 cron 真正的家：dashboard 是操作面板，
// 關掉就沒了；bot 本來就 24/7。兩邊會各跑一個排程器，靠 .claw/cron.json.lock 的跨行程鎖仲裁，
// 同一輪只有一個真的執行。
type botCronRunner struct {
	factory chatbot.EngineFactory
	workDir string
	mu      sync.Mutex // 序列化：一次一個排程 run，避免共用 registry／工具的併發風險
}

// RunJob 跑一輪排程任務：獨立 session（cron-<id>）、終端 reporter。忙碌時回 cron.ErrBusy，
// 排程器就跳過本輪、下輪再試。
//
// ponytail: 每次執行前 Reset——排程任務彼此獨立，且避免同一 session 歷史無限長。代價是只留
// 「最近一次」執行樹（與 dashboard 端一致）。
func (b *botCronRunner) RunJob(sessionID, prompt string) (string, error) {
	if !b.mu.TryLock() {
		return "", cron.ErrBusy
	}
	defer b.mu.Unlock()

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, b.workDir)
	sess.Reset()
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	reporter := engine.NewTerminalReporter()
	eng := b.factory(sess, reporter)
	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		return "", err
	}
	return sess.LastAssistantText(), nil // 供推播摘要
}
