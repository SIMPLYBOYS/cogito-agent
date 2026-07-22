package replay

import (
	"encoding/json"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func TestZZDbg2(t *testing.T) {
	ws := t.TempDir()
	subHistory := []schema.Message{
		{Role: schema.RoleSystem, Content: "你是 code-reviewer"},
		{Role: schema.RoleUser, Content: "審查變更"},
		{Role: schema.RoleAssistant, Content: "先讀檔", ToolCalls: []schema.ToolCall{
			{ID: "s1", Name: "read_file", Arguments: json.RawMessage(`{"path":"auth.go"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "s1", Content: "package auth ..."},
		{Role: schema.RoleAssistant, Content: "審查報告：無明顯問題"},
	}
	_ = WriteSubRun(ws, "c1", SubRun{Prompt: "審查變更", History: subHistory})
	run := Build("sess-1", subagentHistory(), Meta{}, ws)
	t.Logf("Turns=%d", len(run.Turns))
	for i, tn := range run.Turns {
		t.Logf("  turn[%d]: Actions=%d Fan=%d Final=%q Note=%q", i, len(tn.Actions), len(tn.Fan), tn.FinalAnswer, tn.Note)
	}
}
