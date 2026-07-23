package replay

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// TestBuild_CollectsArtifacts 驗證任務產出清單：主 agent 與子 agent 內部的 write_file/edit_file
// 都收得到、去重保序、無寫入的任務為空。
func TestBuild_CollectsArtifacts(t *testing.T) {
	ws := t.TempDir()
	subHistory := []schema.Message{
		{Role: schema.RoleSystem, Content: "你是 implementer"},
		{Role: schema.RoleUser, Content: "建檔"},
		{Role: schema.RoleAssistant, Content: "寫", ToolCalls: []schema.ToolCall{
			{ID: "s1", Name: "write_file", Arguments: json.RawMessage(`{"path":"beta.txt","content":"B"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "s1", Content: "ok"},
		{Role: schema.RoleAssistant, Content: "完成"},
	}
	_ = WriteSubRun(ws, "c9", SubRun{Prompt: "建檔", History: subHistory})

	history := []schema.Message{
		{Role: schema.RoleUser, Content: "產出兩個檔"},
		{Role: schema.RoleAssistant, Content: "先自己寫一個", ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "write_file", Arguments: json.RawMessage(`{"path":"alpha.txt","content":"A"}`)},
			{ID: "c2", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "c1", Content: "ok"},
		{Role: schema.RoleUser, ToolCallID: "c2", Content: "alpha.txt"},
		{Role: schema.RoleAssistant, Content: "再派子 agent", ToolCalls: []schema.ToolCall{
			{ID: "c9", Name: "spawn_subagent", Arguments: json.RawMessage(`{"agent_type":"implementer"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "c9", Content: "子 agent 完成"},
		{Role: schema.RoleAssistant, Content: "改 alpha", ToolCalls: []schema.ToolCall{
			{ID: "c3", Name: "edit_file", Arguments: json.RawMessage(`{"path":"alpha.txt","old":"A","new":"AA"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "c3", Content: "ok"},
		{Role: schema.RoleAssistant, Content: "全部完成"},
	}
	run := Build("s", history, Meta{}, ws)
	if len(run.Tasks) != 1 {
		t.Fatalf("Tasks=%d, 期望 1", len(run.Tasks))
	}
	got := run.Tasks[0].Artifacts
	want := []string{"alpha.txt", "beta.txt"} // 去重（alpha 寫+改只列一次）、含子 agent 的 beta
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Artifacts=%v, 期望 %v", got, want)
	}

	html := string(Fragment(run))
	if !strings.Contains(html, "本次產出") || !strings.Contains(html, "beta.txt") {
		t.Error("Fragment 應渲染產出清單（含子 agent 寫的檔）")
	}

	// 無寫入的 run：不出現產出區塊
	quiet := Build("q", []schema.Message{
		{Role: schema.RoleUser, Content: "查一下"},
		{Role: schema.RoleAssistant, Content: "好了"},
	}, Meta{}, "")
	if len(quiet.Tasks[0].Artifacts) != 0 {
		t.Errorf("無寫入應為空，得到 %v", quiet.Tasks[0].Artifacts)
	}
	if strings.Contains(string(Fragment(quiet)), "本次產出") {
		t.Error("無產出不應渲染產出區塊")
	}
}
