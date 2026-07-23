package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// denyMW 是模擬 policy.Guard 的 Deny 短路（engine 不 import policy，用等價中介層測語意）。
func denyMW(ctx context.Context, call schema.ToolCall, _ tools.ToolHandler) schema.ToolResult {
	return schema.ToolResult{ToolCallID: call.ID, Output: "政策拒絕執行。原因: blacklist", IsError: true, Denied: true}
}

// 修補④（2b）：政策拒絕＝該目標的終止。被拒後 Run 必須立即回錯，不能把拒絕當可重試的
// 觀察再跑下一輪（實測 agent 會改寫命令繞過黑名單）。觀察須已落 history（不留孤兒 tool_use）。
func TestRun_PolicyDenyTerminates(t *testing.T) {
	fp := &fakeProvider{}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	reg.Use(denyMW)
	eng := NewAgentEngine(fp, reg, false, false)

	sess := ctxpkg.NewSession("deny", t.TempDir())
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "刪掉建置產物"})

	err := eng.Run(context.Background(), sess, nil)
	if err == nil || !strings.Contains(err.Error(), "政策拒絕") {
		t.Fatalf("政策拒絕應終止 Run 並回錯，得到 %v", err)
	}
	if fp.calls != 1 {
		t.Errorf("被拒後不該再跑下一輪（重試/繞過），Generate 次數=%d 期望 1", fp.calls)
	}
	// history 完整性：最後一則必須是帶 ToolCallID 的觀察（tool_use/tool_result 成對，不 brick session）
	h := sess.GetWorkingMemory(0)
	last := h[len(h)-1]
	if last.ToolCallID == "" {
		t.Errorf("終止前觀察應已落 history，最後一則卻是 role=%s tcid=%q", last.Role, last.ToolCallID)
	}
	if !strings.Contains(last.Content, "政策拒絕") || strings.Contains(last.Content, "救援") {
		t.Errorf("觀察應為原拒絕訊息（不注入救援指南），得到 %q", last.Content)
	}
}

// deniedTool 模擬 spawn_subagent 把子 agent 內部的政策拒絕以 sentinel 上傳。
type deniedTool struct{}

func (deniedTool) Name() string { return "spawn_subagent" }
func (deniedTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "spawn_subagent"}
}
func (deniedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", tools.ErrPolicyDenied
}

// 修補④（2b）延伸：子 agent 內部被拒 → RunSub 回 ErrPolicyDenied → registry 標 Denied →
// 主迴圈終止。此測驗 sentinel→Denied→終止 這段鏈（RunSub 端的來源由 denied_test 的主測涵蓋語意）。
func TestRun_SubagentDenyPropagates(t *testing.T) {
	fp := &fakeProvider{}
	reg := tools.NewRegistry()
	reg.Register(deniedTool{})
	eng := NewAgentEngine(fp, reg, false, false)

	sess := ctxpkg.NewSession("subdeny", t.TempDir())
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "派子 agent 清產物"})
	// fakeProvider 固定呼叫 noop——這裡讓它呼叫 spawn_subagent
	fp.toolName = "spawn_subagent"

	err := eng.Run(context.Background(), sess, nil)
	if err == nil || !strings.Contains(err.Error(), "政策拒絕") {
		t.Fatalf("子 agent 的政策拒絕應終止主 Run，得到 %v", err)
	}
	if fp.calls != 1 {
		t.Errorf("被拒後主迴圈不該重試/改派，Generate 次數=%d 期望 1", fp.calls)
	}
}
