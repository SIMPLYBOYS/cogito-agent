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
	run := Build("sess-1", subagentHistory(), Meta{Cost: 0.5}, "")

	if run.Query != "幫我重構 auth" {
		t.Errorf("Query 應為任務，got %q", run.Query)
	}
	if !run.HasSubagent {
		t.Error("應偵測到 subagent 委派")
	}
	if len(run.Turns) != 2 {
		t.Fatalf("應 2 個 turn，got %d", len(run.Turns))
	}
	a := run.Turns[0].Actions
	if len(a) != 1 || !a[0].IsSubagent || a[0].AgentType != "code-reviewer" {
		t.Fatalf("turn1 應是 code-reviewer 子 agent 委派，got %+v", a)
	}
	if a[0].CallID != "c1" {
		t.Errorf("委派節點應記 ToolCall.ID（供掛回子深度），got %q", a[0].CallID)
	}
	if a[0].Report != "審查報告：無明顯問題" {
		t.Errorf("子 agent 報告應由 tool_result 回填，got %q", a[0].Report)
	}
	if run.Turns[1].FinalAnswer != "完成，已審查" {
		t.Errorf("turn2 應是最終回答，got %q", run.Turns[1].FinalAnswer)
	}
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
	run := Build("s2", h, Meta{}, "")
	if run.HasSubagent {
		t.Error("純主 agent run 不該標 subagent")
	}
	if run.Turns[0].Actions[0].Observation != "2026 ..." {
		t.Error("bash 的觀察應回填")
	}
}

// M2：sub-history 落地 + Build 用 callID 掛回子 agent 內部。
func TestBuild_SubagentDepth(t *testing.T) {
	ws := t.TempDir()
	// 子 agent 的內部 history（它自己的 thinking→action→observation）
	subHistory := []schema.Message{
		{Role: schema.RoleSystem, Content: "你是 code-reviewer"},
		{Role: schema.RoleUser, Content: "審查變更"},
		{Role: schema.RoleAssistant, Content: "先讀檔", ToolCalls: []schema.ToolCall{
			{ID: "s1", Name: "read_file", Arguments: json.RawMessage(`{"path":"auth.go"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "s1", Content: "package auth ..."},
		{Role: schema.RoleAssistant, Content: "審查報告：無明顯問題"},
	}
	// callID = 主 session 那個 spawn_subagent 的 ToolCall.ID（"c1"）
	if err := WriteSubRun(ws, "c1", SubRun{Prompt: "審查變更", History: subHistory}); err != nil {
		t.Fatal(err)
	}

	// 給 workDir → 應掛回子深度
	run := Build("sess-1", subagentHistory(), Meta{}, ws)
	sub := run.Turns[0].Actions[0]
	if len(sub.SubTurns) == 0 {
		t.Fatal("有落地 sub-history 時，委派節點應掛回子 agent 內部 turns")
	}
	// 子 agent 內部應看得到它自己的 read_file + 審查結論
	var found bool
	for _, tr := range sub.SubTurns {
		for _, a := range tr.Actions {
			if a.Tool == "read_file" {
				found = true
			}
		}
	}
	if !found {
		t.Error("子 agent 內部 turns 應含它的 read_file 調用")
	}

	// 不給 workDir → 只到委派層（不掛子深度），保持 M1 行為
	shallow := Build("sess-1", subagentHistory(), Meta{}, "")
	if len(shallow.Turns[0].Actions[0].SubTurns) != 0 {
		t.Error("未給 workDir 不該載入子深度")
	}
}

func TestFragment_RendersAndEscapes(t *testing.T) {
	run := Build("s1", subagentHistory(), Meta{Cost: 0.5}, "")
	out := string(Fragment(run))

	for _, want := range []string{"幫我重構 auth", "委派子 agent", "code-reviewer", "審查報告", "最終回答", "subagent 協同"} {
		if !strings.Contains(out, want) {
			t.Errorf("Fragment 應含 %q", want)
		}
	}

	evil := []schema.Message{
		{Role: schema.RoleUser, Content: "x"},
		{Role: schema.RoleAssistant, Content: "<script>alert(1)</script>"},
	}
	e := string(Fragment(Build("s3", evil, Meta{}, "")))
	if strings.Contains(e, "<script>alert(1)</script>") {
		t.Error("模型/工具文字未被跳脫——XSS 風險")
	}
	if !strings.Contains(e, "&lt;script&gt;") {
		t.Error("應輸出跳脫後的 &lt;script&gt;")
	}
}

// 子深度也要出現在 Fragment（可展開的子 agent 內部）。
func TestFragment_RendersSubDepth(t *testing.T) {
	ws := t.TempDir()
	subHistory := []schema.Message{
		{Role: schema.RoleUser, Content: "審查"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "s1", Name: "read_file", Arguments: json.RawMessage(`{"path":"x.go"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "s1", Content: "內容"},
		{Role: schema.RoleAssistant, Content: "OK"},
	}
	_ = WriteSubRun(ws, "c1", SubRun{History: subHistory})
	out := string(Fragment(Build("s", subagentHistory(), Meta{}, ws)))
	if !strings.Contains(out, "子 agent 內部") {
		t.Error("Fragment 應含可展開的子 agent 內部")
	}
	if !strings.Contains(out, "read_file") {
		t.Error("子 agent 內部應渲染它的 read_file")
	}
}
