//go:build !windows

package chatbot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// /stop 走完整條路（Core.Dispatch → 取消 Run → eng.StopBackground → 工具的 BackgroundStopper）：
// 背景指令是真的行程、背景子 agent 是真的 goroutine，只有模型是照劇本出牌的替身。
// cogito-agent#1：3165061 只有元件層的單元測試，這條鏈沒實際跑過。

// stopScript：第一輪同時開一條背景指令與一個背景子 agent；之後依 finish 決定是長考到被取消、還是直接收工。
type stopScript struct {
	mu      sync.Mutex
	n       int
	pidFile string
	finish  bool // true＝第二輪就收工（背景工作留著）；false＝長考到 /stop
}

func (p *stopScript) Generate(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	p.mu.Lock()
	p.n++
	n := p.n
	p.mu.Unlock()
	if n == 1 {
		cmd, _ := json.Marshal(map[string]string{"command": "echo $$ > " + p.pidFile + "; exec sleep 300"})
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "bash_background", Arguments: cmd},
			{ID: "c2", Name: "spawn_subagent", Arguments: []byte(`{"task_prompt":"慢慢查","background":true}`)},
		}}, nil
	}
	if p.finish {
		return &schema.Message{Role: schema.RoleAssistant, Content: "先這樣，背景的讓它跑"}, nil
	}
	<-ctx.Done() // 長考中，等 /stop
	return nil, ctx.Err()
}
func (p *stopScript) MaxContextTokens() int { return 200000 }
func (p *stopScript) ModelName() string     { return "scripted" }

// waitRunner：背景子 agent 的替身，跑到被取消為止。
type waitRunner struct{ started, cancelled chan struct{} }

func (r *waitRunner) RunSub(ctx context.Context, _ tools.SubTask) (string, error) {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	return "", ctx.Err()
}

type stopHarness struct {
	core   *Core
	runner *waitRunner
	pid    int
	sent   func() string
}

func startBackgroundTask(t *testing.T, finish bool) *stopHarness {
	t.Helper()
	t.Setenv("COGITO_ALLOWED_USERS", "u1")
	t.Setenv("COGITO_ADMIN_USERS", "u1")
	t.Setenv("COGITO_OFFICE_URL", "")
	dir := t.TempDir()
	h := &stopHarness{runner: &waitRunner{started: make(chan struct{}), cancelled: make(chan struct{})}}
	prov := &stopScript{pidFile: filepath.Join(dir, "pid"), finish: finish}
	factory := func(s *ctxpkg.Session, _ engine.Reporter) *engine.AgentEngine {
		reg := tools.NewRegistry()
		for _, tl := range tools.NewTaskTools(tools.NewTaskManager(sandbox.HostExecutor{}, s.WorkDir)) {
			reg.Register(tl)
		}
		sub := tools.NewSubagentTool(h.runner, tools.NewRegistry(), nil, dir)
		reg.Register(sub)
		for _, bt := range sub.BackgroundTools() {
			reg.Register(bt)
		}
		return engine.NewAgentEngine(prov, reg, false, false)
	}
	var mu sync.Mutex
	var msgs []string
	platform := "stopbg" + strconv.FormatBool(finish)
	h.core = NewCore(platform, dir, factory, func(_, text string) { mu.Lock(); msgs = append(msgs, text); mu.Unlock() })
	h.sent = func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(msgs, "\n") }

	h.core.Dispatch("ch1", "u1", "開背景工作")
	deadline := time.Now().Add(5 * time.Second)
	for h.pid == 0 && time.Now().Before(deadline) {
		if b, err := os.ReadFile(prov.pidFile); err == nil {
			h.pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.pid == 0 {
		t.Fatalf("背景指令沒起來；訊息：%s", h.sent())
	}
	t.Cleanup(func() { _ = syscall.Kill(h.pid, syscall.SIGKILL) })
	select {
	case <-h.runner.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("背景子 agent 沒起來；訊息：%s", h.sent())
	}
	return h
}

// 驗收：/stop 之後 5 秒內，那次任務開出的背景子 agent 與背景指令行程都不在了。
func (h *stopHarness) assertStopped(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(h.pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(h.pid, 0) == nil {
		t.Errorf("/stop 之後 5 秒，背景指令行程 %d 還活著；訊息：%s", h.pid, h.sent())
	}
	select {
	case <-h.runner.cancelled:
	case <-time.After(5 * time.Second):
		t.Errorf("/stop 之後 5 秒，背景子 agent 沒被取消；訊息：%s", h.sent())
	}
}

// 主任務還在跑時按 /stop：背景指令與背景子 agent 一起收，回報寫明收了什麼。
func TestStopKillsBackgroundWorkOfRunningTask(t *testing.T) {
	h := startBackgroundTask(t, false)
	h.core.Dispatch("ch1", "u1", "/stop")
	h.assertStopped(t)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.sent(), "🛑 已中止本次任務") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s := h.sent(); !strings.Contains(s, "一併收掉 1 個背景子 agent、1 個背景指令") {
		t.Errorf("收尾訊息沒寫明一併收掉了什麼：%s", s)
	}
}

// 主任務已經收工、背景工作還在跑：這時的 /stop 沒有任務可取消，背景工作的管理器又是每輪各一個，
// 下一輪的引擎看不到。修正前它們會一路跑到 claw 關掉；現在 core 記下收工時還在跑的那一輪，/stop 一併收。
func TestStopKillsBackgroundWorkAfterTaskFinished(t *testing.T) {
	h := startBackgroundTask(t, true)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.sent(), "✅ 任務完成") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(h.sent(), "✅ 任務完成") {
		t.Fatalf("主任務應該先收工：%s", h.sent())
	}
	h.core.Dispatch("ch1", "u1", "/stop")
	h.assertStopped(t)
	if s := h.sent(); !strings.Contains(s, "目前沒有正在執行的任務；先前任務留在背景的 1 個背景子 agent、1 個背景指令 已收掉") {
		t.Errorf("沒有任務在跑時的 /stop 要講清楚收了什麼：%s", s)
	}
}
