package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgent(t *testing.T, base, name, md string) {
	t.Helper()
	dir := filepath.Join(base, ".claw", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentLoader_LoadAndIndex(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "reviewer",
		"---\nname: code-reviewer\ndescription: 從正確性/安全審查程式\ntools: [read_file, bash]\n---\n你是資深 code reviewer，只讀不改。")
	writeAgent(t, dir, "planner",
		"---\nname: planner\ndescription: 拆解任務成步驟\n---\n你負責規劃。")

	l := NewAgentLoader(dir)

	def, err := l.Load("reviewer")
	if err != nil {
		t.Fatalf("Load 失敗: %v", err)
	}
	if def.Name != "code-reviewer" || def.Description == "" {
		t.Errorf("frontmatter 解析錯: %+v", def)
	}
	if len(def.Tools) != 2 || def.Tools[0] != "read_file" || def.Tools[1] != "bash" {
		t.Errorf("tools 應解析成 list，got %v", def.Tools)
	}
	if !strings.Contains(def.Prompt, "資深 code reviewer") {
		t.Errorf("body 應為 system prompt，got %q", def.Prompt)
	}

	// 未宣告 tools → 空（沿用預設工具集）
	p, _ := l.Load("planner")
	if len(p.Tools) != 0 {
		t.Errorf("未宣告 tools 應為空，got %v", p.Tools)
	}

	idx := l.Index()
	if !strings.Contains(idx, "code-reviewer") || !strings.Contains(idx, "planner") {
		t.Errorf("Index 應列出所有 agent，got:\n%s", idx)
	}
}

func TestAgentLoader_NotFoundAndEmpty(t *testing.T) {
	l := NewAgentLoader(t.TempDir())
	if _, err := l.Load("nope"); err == nil {
		t.Error("找不到的 agent 應回錯")
	}
	if l.Index() != "" {
		t.Error("無 .claw/agents 時 Index 應為空（退回預設探路者）")
	}
	// 防路徑穿越：Load 只取檔名片段
	if _, err := l.Load("../../etc/passwd"); err == nil {
		t.Error("路徑穿越應被擋（當成不存在的檔名）")
	}
}
