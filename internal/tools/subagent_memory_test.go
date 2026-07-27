package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAgentMemory 在 .claw/agents/<name>/memory/ 放一筆 per-agent 記憶記錄。
func writeAgentMemory(t *testing.T, base, agent, recName, body string) {
	t.Helper()
	dir := filepath.Join(base, ".claw", "agents", agent, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + recName + "\ndescription: 過往沉澱\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, recName+".md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 🧭 具名 agent 記憶（讀半邊）：spawn agent_type=X 時，X 的 per-agent 記憶被注入子 agent 的 system prompt。
func TestSubagent_PerAgentMemoryInjected(t *testing.T) {
	dir := t.TempDir()
	writeAgentDef(t, dir, "scout", "---\nname: scout\ndescription: 探路者\n---\n你是探路者。")
	writeAgentMemory(t, dir, "scout", "prev-finding", "上次查過：auth 模組用 JWT，token 放 header X-Auth。")

	fr := &fakeRunner{}
	st := NewSubagentTool(fr, NewRegistry(), nil, dir)
	if _, err := st.Execute(context.Background(), []byte(`{"task_prompt":"再查一次 auth","agent_type":"scout"}`)); err != nil {
		t.Fatalf("Execute 失敗: %v", err)
	}
	if !strings.Contains(fr.gotSysPrompt, "你是探路者") {
		t.Error("角色 prompt 應在 system prompt 內")
	}
	if !strings.Contains(fr.gotSysPrompt, "auth 模組用 JWT") {
		t.Errorf("per-agent 記憶正文應注入 system prompt，got %q", fr.gotSysPrompt)
	}
	if !strings.Contains(fr.gotSysPrompt, "長期記憶") {
		t.Error("應有記憶區塊標頭")
	}
}

// 沒有 per-agent 記憶目錄時：只有角色 prompt，不注入記憶區塊（不同 agent 記憶不互見的基礎）。
func TestSubagent_NoPerAgentMemory(t *testing.T) {
	dir := t.TempDir()
	writeAgentDef(t, dir, "planner", "---\nname: planner\ndescription: 規劃者\n---\n你是規劃者。")
	// planner 沒有記憶；scout 有——驗證不會串到 planner
	writeAgentMemory(t, dir, "scout", "scout-only", "scout 的私有記憶")

	fr := &fakeRunner{}
	st := NewSubagentTool(fr, NewRegistry(), nil, dir)
	if _, err := st.Execute(context.Background(), []byte(`{"task_prompt":"規劃","agent_type":"planner"}`)); err != nil {
		t.Fatalf("Execute 失敗: %v", err)
	}
	if strings.Contains(fr.gotSysPrompt, "長期記憶") || strings.Contains(fr.gotSysPrompt, "scout 的私有記憶") {
		t.Errorf("planner 不該看到記憶區塊或 scout 的記憶，got %q", fr.gotSysPrompt)
	}
}
