package engine

import (
	"context"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/replay"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// finalProvider 第一輪就回「最終答案」（無 tool call）→ RunSub 立刻收尾並落地子 history。
type finalProvider struct{}

func (finalProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: "子 agent 報告：已完成"}, nil
}
func (finalProvider) MaxContextTokens() int { return 200000 }
func (finalProvider) ModelName() string     { return "test" }

// M2：RunSub 在 ctx 帶 session workDir + spawn_subagent 的 callID 時，把子 agent 內部 history
// 落地成 subagents/<callID>.json（供 dashboard 掛回主節點）。
func TestRunSub_PersistsSubHistory(t *testing.T) {
	sess := ctxpkg.NewSession("sub-persist", t.TempDir())
	eng := NewAgentEngine(finalProvider{}, newTestRegistry(), false, false)

	ctx := tools.WithCallID(WithSession(context.Background(), sess), "call-abc")
	report, err := eng.RunSub(ctx, tools.SubTask{Prompt: "去查 X", Registry: tools.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	if report == "" {
		t.Error("RunSub 應回報告字串（主流程不受落地影響）")
	}
	sr, ok := replay.LoadSubRun(sess.WorkDir, "call-abc")
	if !ok {
		t.Fatal("應把子 agent 內部 history 落地成 subagents/call-abc.json")
	}
	if sr.Prompt != "去查 X" || len(sr.History) < 2 {
		t.Errorf("落地內容不對：prompt=%q historyLen=%d", sr.Prompt, len(sr.History))
	}
}

// 背景子 agent 走 detached ctx（無 session、無 callID）→ 不落地、也不崩，行為與過去一致。
func TestRunSub_NoSessionNoWrite(t *testing.T) {
	eng := NewAgentEngine(finalProvider{}, newTestRegistry(), false, false)
	report, err := eng.RunSub(context.Background(), tools.SubTask{Prompt: "背景", Registry: tools.NewRegistry()})
	if err != nil {
		t.Fatalf("detached ctx 下 RunSub 不該出錯：%v", err)
	}
	if report == "" {
		t.Error("仍應正常回報告")
	}
}
