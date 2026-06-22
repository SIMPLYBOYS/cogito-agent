package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, base, name, desc, body string) {
	t.Helper()
	dir := filepath.Join(base, ".claw", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 漸進式暴露：LoadIndex 只給元數據（名稱+描述），不含正文；正文由 ReadSkill 按需取。
func TestSkillLoader_IndexAndRead(t *testing.T) {
	base := t.TempDir()
	writeSkill(t, base, "git-workflow", "提交與 PR 流程", "BODY-GIT-步驟一二三")
	writeSkill(t, base, "deploy", "部署到生產", "BODY-DEPLOY-internal")
	l := NewSkillLoader(base)

	idx := l.LoadIndex()
	if !strings.Contains(idx, "git-workflow") || !strings.Contains(idx, "提交與 PR 流程") {
		t.Errorf("索引應含技能名與描述: %s", idx)
	}
	if strings.Contains(idx, "BODY-GIT") || strings.Contains(idx, "BODY-DEPLOY") {
		t.Error("索引不應包含技能正文（漸進式暴露的核心）")
	}

	body, err := l.ReadSkill("git-workflow")
	if err != nil {
		t.Fatalf("ReadSkill 失敗: %v", err)
	}
	if !strings.Contains(body, "BODY-GIT-步驟一二三") {
		t.Errorf("ReadSkill 應回傳正文，got %q", body)
	}

	if _, err := l.ReadSkill("不存在的技能"); err == nil {
		t.Error("未知技能應回 error")
	}
}

func TestSkillLoader_EmptyWhenNoSkills(t *testing.T) {
	if got := NewSkillLoader(t.TempDir()).LoadIndex(); got != "" {
		t.Errorf("無技能時索引應為空，got %q", got)
	}
}
