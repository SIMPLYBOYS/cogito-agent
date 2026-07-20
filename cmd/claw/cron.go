package main

import (
	"context"
	"sync"

	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/cron"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/policy"
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
	// 標記無人值守：排程沒有人在現場。少了這個，高危操作會把審批請求推去一個不存在的頻道
	//（session id 是 cron-<id> 而非真頻道），然後卡到逾時才拒絕——慢，而且只是碰巧安全。
	ctx := policy.WithUnattended(context.Background())
	if err := eng.Run(ctx, sess, reporter); err != nil {
		return "", err
	}
	return sess.LastAssistantText(), nil // 供推播摘要
}
