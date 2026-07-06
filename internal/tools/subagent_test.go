package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner 捕獲傳給 RunSub 的參數，驗證技能正文/角色 prompt 是否被注入子 agent。
type fakeRunner struct {
	gotTask      string
	gotSkillBody string
	gotSysPrompt string
	called       bool
}

func (f *fakeRunner) RunSub(_ context.Context, taskPrompt, skillBody, systemPrompt string, _ Registry, _ interface{}) (string, error) {
	f.called = true
	f.gotTask = taskPrompt
	f.gotSkillBody = skillBody
	f.gotSysPrompt = systemPrompt
	return "report-ok", nil
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

// 未指定 agent_type → systemPrompt 為空（RunSub 回退預設探路者）。
func TestSubagent_DefaultExplorer(t *testing.T) {
	fr := &fakeRunner{}
	st := NewSubagentTool(fr, NewRegistry(), nil, t.TempDir())
	if _, err := st.Execute(context.Background(), []byte(`{"task_prompt":"找 bug"}`)); err != nil {
		t.Fatal(err)
	}
	if fr.gotSysPrompt != "" {
		t.Errorf("未指定 agent_type 時 systemPrompt 應為空，got %q", fr.gotSysPrompt)
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
