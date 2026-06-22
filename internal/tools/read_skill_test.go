package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadSkillTool(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, ".claw", "skills", "foo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: foo\ndescription: d\n---\nSECRET-BODY-XYZ"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadSkillTool(base)
	out, err := tool.Execute(context.Background(), []byte(`{"name":"foo"}`))
	if err != nil {
		t.Fatalf("Execute 失敗: %v", err)
	}
	if out != "SECRET-BODY-XYZ" {
		t.Errorf("應回傳技能正文，got %q", out)
	}

	if _, err := tool.Execute(context.Background(), []byte(`{"name":"nope"}`)); err == nil {
		t.Error("未知技能應回 error")
	}
}
