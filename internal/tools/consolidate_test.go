package tools

import (
	"context"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func TestFirstUserMessage_SkipsPlaceholderAndToolResult(t *testing.T) {
	h := []schema.Message{
		{Role: schema.RoleUser, Content: "[系統佔位符] 斷點"},
		{Role: schema.RoleUser, ToolCallID: "x", Content: "工具結果"},
		{Role: schema.RoleAssistant, Content: "想法"},
		{Role: schema.RoleUser, Content: "真正的任務"},
	}
	if got := firstUserMessage(h); got != "真正的任務" {
		t.Errorf("應取第一條真正使用者訊息，got %q", got)
	}
}

// 三個自我進化旗標都關時，consolidate 不呼叫任何 LLM（provider 傳 nil 也不會被用到），回「無內容」。
func TestConsolidate_NoFlagsNoLLM(t *testing.T) {
	t.Setenv("COGITO_SKILL_SYNTH", "")
	t.Setenv("COGITO_MEMORY_SYNTH", "")
	t.Setenv("COGITO_KG_SYNTH", "")
	sess := ctxpkg.NewSession("c1", t.TempDir())
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "做點事"})

	tool := NewConsolidateTool(nil, t.TempDir(), sess) // provider=nil：旗標全關時不會被呼叫
	out, err := tool.Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "沒有") {
		t.Errorf("旗標全關應回無內容訊息，got %q", out)
	}
}
