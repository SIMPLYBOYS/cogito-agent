package main

import (
	"context"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/cron"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/policy"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// dashCronRunner 把 dashboard 已組好的 operator agent 接到 cron.Runner 介面上。
type dashCronRunner struct{ chat *chatRunner }

// RunJob 同步跑一輪排程任務：獨立 session、終端 reporter（不打 operator chat 的 SSE 串流，
// 免得排程輸出污染對話）。與 operator chat 共用同一把鎖——本行程一次只跑一個 agent run，
// 避免共用 registry／工具的併發風險；忙碌時回 cron.ErrBusy 讓排程器下輪再試。
//
// ponytail: 每次執行前 Reset——排程任務彼此獨立，且避免同一 session 歷史無限長。代價是只留
// 「最近一次」執行樹；要保留每次歷史就得一次一 session（sessions 會無限長），需要時再改。
func (d dashCronRunner) RunJob(sessionID, prompt string) (string, error) {
	c := d.chat
	if !c.mu.TryLock() {
		return "", cron.ErrBusy
	}
	defer c.mu.Unlock()

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, c.workDir)
	sess.Reset()
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})
	// 標記無人值守：排程沒有人在現場，需審批的高危操作一律拒絕而非等一個不會來的人。
	ctx := policy.WithUnattended(context.Background())
	if err := c.eng.Run(ctx, sess, engine.NewTerminalReporter()); err != nil {
		return "", err
	}
	return sess.LastAssistantText(), nil // 供推播摘要
}
