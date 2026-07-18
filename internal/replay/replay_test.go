package replay

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func subagentHistory() []schema.Message {
	return []schema.Message{
		{Role: schema.RoleSystem, Content: "靜態系統 prompt（應被略過）"},
		{Role: schema.RoleUser, Content: "幫我重構 auth"},
		{Role: schema.RoleAssistant, Content: "先派 reviewer 審一遍", ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "spawn_subagent", Arguments: json.RawMessage(`{"agent_type":"code-reviewer","task_prompt":"審查變更"}`)},
		}, Usage: &schema.Usage{PromptTokens: 100, CompletionTokens: 20}},
		{Role: schema.RoleUser, ToolCallID: "c1", Content: "審查報告：無明顯問題"},
		{Role: schema.RoleAssistant, Content: "完成，已審查"},
	}
}

func TestBuild_ReconstructsTurnsAndSubagent(t *testing.T) {
	run := Build("sess-1", subagentHistory(), Meta{Cost: 0.5})

	if run.Query != "幫我重構 auth" {
		t.Errorf("Query 應為任務，got %q", run.Query)
	}
	if !run.HasSubagent {
		t.Error("應偵測到 subagent 委派")
	}
	// system 略過、query 不算 turn → 剩 2 個 assistant turn
	if len(run.Turns) != 2 {
		t.Fatalf("應 2 個 turn，got %d", len(run.Turns))
	}
	// turn1：spawn_subagent，帶 agent_type + 回填的報告
	a := run.Turns[0].Actions
	if len(a) != 1 || !a[0].IsSubagent || a[0].AgentType != "code-reviewer" {
		t.Fatalf("turn1 應是 code-reviewer 子 agent 委派，got %+v", a)
	}
	if a[0].Report != "審查報告：無明顯問題" {
		t.Errorf("子 agent 報告應由 tool_result 回填，got %q", a[0].Report)
	}
	// turn2：最終回答
	if run.Turns[1].FinalAnswer != "完成，已審查" {
		t.Errorf("turn2 應是最終回答，got %q", run.Turns[1].FinalAnswer)
	}
	// 編號 1,2
	if run.Turns[0].Index != 1 || run.Turns[1].Index != 2 {
		t.Errorf("turn 應編號 1,2，got %d,%d", run.Turns[0].Index, run.Turns[1].Index)
	}
}

func TestBuild_MainAgentOnly(t *testing.T) {
	h := []schema.Message{
		{Role: schema.RoleUser, Content: "看一下時間"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "b1", Name: "bash", Arguments: json.RawMessage(`{"command":"date"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "b1", Content: "2026 ..."},
		{Role: schema.RoleAssistant, Content: "現在是 2026"},
	}
	run := Build("s2", h, Meta{})
	if run.HasSubagent {
		t.Error("純主 agent run 不該標 subagent")
	}
	if run.Turns[0].Actions[0].Observation != "2026 ..." {
		t.Error("bash 的觀察應回填")
	}
}

func TestFragment_RendersAndEscapes(t *testing.T) {
	run := Build("s1", subagentHistory(), Meta{Cost: 0.5})
	out := string(Fragment(run))

	for _, want := range []string{"幫我重構 auth", "spawn_subagent", "code-reviewer", "審查報告", "最終回答", "有 subagent 協同"} {
		if !strings.Contains(out, want) {
			t.Errorf("Fragment 應含 %q", want)
		}
	}

	// 不受信文字必須被跳脫（html/template）——注入 <script> 不得原樣輸出
	evil := []schema.Message{
		{Role: schema.RoleUser, Content: "x"},
		{Role: schema.RoleAssistant, Content: "<script>alert(1)</script>"},
	}
	e := string(Fragment(Build("s3", evil, Meta{})))
	if strings.Contains(e, "<script>alert(1)</script>") {
		t.Error("模型/工具文字未被跳脫——XSS 風險")
	}
	if !strings.Contains(e, "&lt;script&gt;") {
		t.Error("應輸出跳脫後的 &lt;script&gt;")
	}
}
