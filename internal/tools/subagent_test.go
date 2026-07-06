package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner 捕獲傳給 RunSub 的參數，驗證技能正文/角色 prompt/工具集/模型是否正確傳入子 agent。
type fakeRunner struct {
	gotTask      string
	gotSkillBody string
	gotSysPrompt string
	gotModel     string
	gotMaxTokens int
	gotTools     []string // 子 agent 實際拿到的工具名
	called       bool
}

func (f *fakeRunner) RunSub(_ context.Context, task SubTask) (string, error) {
	f.called = true
	f.gotTask = task.Prompt
	f.gotSkillBody = task.SkillBody
	f.gotSysPrompt = task.SystemPrompt
	f.gotModel = task.Model
	f.gotMaxTokens = task.MaxTokens
	f.gotTools = nil
	for _, d := range task.Registry.GetAvailableTools() {
		f.gotTools = append(f.gotTools, d.Name)
	}
	return "report-ok", nil
}

// superReg 建一個含 read/bash/write/edit 的超集註冊表（用 stubTool），模擬 cmd/claw 傳入的池。
func superReg() Registry {
	r := NewRegistry()
	for _, n := range []string{"read_file", "bash", "write_file", "edit_file"} {
		r.Register(stubTool{name: n})
	}
	return r
}

func hasTool(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}

func writeSkill(t *testing.T, base string) {
	t.Helper()
	dir := filepath.Join(base, ".claw", "skills", "pdf")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: pdf-expert\ndescription: 處理 PDF\n---\n這是完整的 PDF 操作指南正文。"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSubagent_SkillBinding(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir)

	// 綁定有效技能 → 正文注入子 agent
	fr := &fakeRunner{}
	st := NewSubagentTool(fr, NewRegistry(), nil, dir)
	out, err := st.Execute(context.Background(), []byte(`{"task_prompt":"做事","skill":"pdf-expert"}`))
	if err != nil {
		t.Fatalf("Execute 失敗: %v", err)
	}
	if !strings.Contains(fr.gotSkillBody, "完整的 PDF 操作指南") {
		t.Errorf("子 agent 應收到技能正文，got %q", fr.gotSkillBody)
	}
	if !strings.Contains(out, "report-ok") {
		t.Errorf("應回傳子 agent 報告，got %q", out)
	}
}

func TestSubagent_NoSkill(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir)

	fr := &fakeRunner{}
	st := NewSubagentTool(fr, NewRegistry(), nil, dir)
	if _, err := st.Execute(context.Background(), []byte(`{"task_prompt":"做事"}`)); err != nil {
		t.Fatalf("Execute 失敗: %v", err)
	}
	if fr.gotSkillBody != "" {
		t.Errorf("未綁定技能時 skillBody 應為空，got %q", fr.gotSkillBody)
	}
}

func writeAgentDef(t *testing.T, base, name, md string) {
	t.Helper()
	dir := filepath.Join(base, ".claw", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 具名 agent：agent_type 指定後，該 agent 的角色 prompt 被注入子 agent。
func TestSubagent_NamedAgent(t *testing.T) {
	dir := t.TempDir()
	writeAgentDef(t, dir, "reviewer",
		"---\nname: code-reviewer\ndescription: 審查\ntools: [read_file]\n---\n你是資深 code reviewer，只讀不改。")

	fr := &fakeRunner{}
	st := NewSubagentTool(fr, NewRegistry(), nil, dir)
	out, err := st.Execute(context.Background(), []byte(`{"task_prompt":"審這個 PR","agent_type":"reviewer"}`))
	if err != nil {
		t.Fatalf("Execute 失敗: %v", err)
	}
	if !strings.Contains(fr.gotSysPrompt, "資深 code reviewer") {
		t.Errorf("應注入具名 agent 的角色 prompt，got %q", fr.gotSysPrompt)
	}
	if !strings.Contains(out, "report-ok") || !fr.called {
		t.Error("應拉起子 agent 並回報告")
	}
}

// 未指定 agent_type → systemPrompt 為空（回退預設探路者），且【只拿唯讀工具】——即便傳入的是含
// write/edit 的超集，預設探路者也絕不該拿到寫入能力。
func TestSubagent_DefaultExplorerIsReadOnly(t *testing.T) {
	fr := &fakeRunner{}
	st := NewSubagentTool(fr, superReg(), nil, t.TempDir())
	if _, err := st.Execute(context.Background(), []byte(`{"task_prompt":"找 bug"}`)); err != nil {
		t.Fatal(err)
	}
	if fr.gotSysPrompt != "" {
		t.Errorf("未指定 agent_type 時 systemPrompt 應為空，got %q", fr.gotSysPrompt)
	}
	if !hasTool(fr.gotTools, "read_file") || !hasTool(fr.gotTools, "bash") {
		t.Errorf("預設應有 read_file+bash，got %v", fr.gotTools)
	}
	if hasTool(fr.gotTools, "write_file") || hasTool(fr.gotTools, "edit_file") {
		t.Errorf("預設探路者不該拿到 write/edit（安全），got %v", fr.gotTools)
	}
}

// 具名【實作型】agent 宣告 write/edit → 子 agent 拿得到寫入工具（opt-in）。
func TestSubagent_WriteAgentGetsWriteTools(t *testing.T) {
	dir := t.TempDir()
	writeAgentDef(t, dir, "implementer",
		"---\nname: implementer\ndescription: 實作\ntools: [read_file, bash, write_file, edit_file]\n---\n你是實作工程師，依規格改檔。")
	fr := &fakeRunner{}
	st := NewSubagentTool(fr, superReg(), nil, dir)
	if _, err := st.Execute(context.Background(), []byte(`{"task_prompt":"實作 X","agent_type":"implementer"}`)); err != nil {
		t.Fatal(err)
	}
	if !hasTool(fr.gotTools, "write_file") || !hasTool(fr.gotTools, "edit_file") {
		t.Errorf("實作型 agent 應拿到 write/edit，got %v", fr.gotTools)
	}
}

// 具名 agent 宣告 model/effort → 傳入 SubTask 的 Model 與 MaxTokens（effort:high→8192）。
func TestSubagent_ModelAndEffort(t *testing.T) {
	dir := t.TempDir()
	writeAgentDef(t, dir, "reviewer",
		"---\nname: reviewer\ndescription: 審查\nmodel: claude-opus-4-8\neffort: high\ntools: [read_file, bash]\n---\n你是 reviewer。")
	fr := &fakeRunner{}
	st := NewSubagentTool(fr, superReg(), nil, dir)
	if _, err := st.Execute(context.Background(), []byte(`{"task_prompt":"審","agent_type":"reviewer"}`)); err != nil {
		t.Fatal(err)
	}
	if fr.gotModel != "claude-opus-4-8" {
		t.Errorf("應傳入 model，got %q", fr.gotModel)
	}
	if fr.gotMaxTokens != 8192 {
		t.Errorf("effort:high 應→8192 maxTokens，got %d", fr.gotMaxTokens)
	}
}

// 不存在的 agent_type → error-as-observation，不拉起子 agent。
func TestSubagent_UnknownAgent(t *testing.T) {
	fr := &fakeRunner{}
	st := NewSubagentTool(fr, NewRegistry(), nil, t.TempDir())
	out, err := st.Execute(context.Background(), []byte(`{"task_prompt":"x","agent_type":"nope"}`))
	if err != nil {
		t.Fatalf("應為 error-as-observation: %v", err)
	}
	if !strings.Contains(out, "載入 agent 失敗") || fr.called {
		t.Errorf("未知 agent 應提示失敗且不拉起子 agent，got %q called=%v", out, fr.called)
	}
}

func TestSubagent_UnknownSkill(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir)

	fr := &fakeRunner{}
	st := NewSubagentTool(fr, NewRegistry(), nil, dir)
	// 綁定不存在的技能 → error-as-observation（無 Go error），且不應拉起子 agent
	out, err := st.Execute(context.Background(), []byte(`{"task_prompt":"做事","skill":"nope"}`))
	if err != nil {
		t.Fatalf("應為 error-as-observation，不回 Go error: %v", err)
	}
	if !strings.Contains(out, "綁定技能失敗") {
		t.Errorf("應提示綁定失敗，got %q", out)
	}
	if fr.called {
		t.Error("綁定技能失敗時不應調用 RunSub")
	}
}
