package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodSkill = `---
name: run-go-tests
description: 當需要驗證 Go 變更時
---
1. 先 go build ./...
2. 再 go test ./...
3. 失敗就讀錯誤訊息逐一修`

func TestGate_GoodSkillPasses(t *testing.T) {
	p := writeSkillFile(t, t.TempDir(), "s.md", goodSkill)
	res, err := Gate(p)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Errorf("良好技能應通過，issues=%v", res.Issues)
	}
}

func TestGate_DangerousBodyRejected(t *testing.T) {
	bad := "---\nname: clean\ndescription: 清理\n---\n為了清乾淨，執行 `sudo rm -rf /tmp/*` 然後重來。"
	p := writeSkillFile(t, t.TempDir(), "s.md", bad)
	res, _ := Gate(p)
	if res.Passed {
		t.Fatal("含 rm -rf / sudo 的技能必須被擋")
	}
	joined := strings.Join(res.Issues, " ")
	if !strings.Contains(joined, "危險") {
		t.Errorf("應標記危險模式，got %v", res.Issues)
	}
}

func TestGate_MissingFrontmatterAndShortBody(t *testing.T) {
	p := writeSkillFile(t, t.TempDir(), "s.md", "就這樣")
	res, _ := Gate(p)
	if res.Passed {
		t.Error("無 frontmatter + 正文過短應不通過")
	}
}

func TestPromote_MovesOnPass(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "skills-proposed", "run-go-tests")
	active := filepath.Join(base, "skills")
	writeSkillFile(t, skillDir, "SKILL.md", goodSkill)

	res, err := Promote(skillDir, active)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Fatalf("應通過並晉升，issues=%v", res.Issues)
	}
	// 原資料夾應已移走、新資料夾應在 active
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Error("晉升後提案資料夾應已移走")
	}
	if _, err := os.Stat(filepath.Join(active, "run-go-tests", "SKILL.md")); err != nil {
		t.Error("晉升後應出現在 active/<name>/SKILL.md")
	}
}

func TestPromote_RefusesOnFail(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "skills-proposed", "x")
	active := filepath.Join(base, "skills")
	bad := "---\nname: x\ndescription: d\n---\nsudo rm -rf /"
	writeSkillFile(t, skillDir, "SKILL.md", bad)

	res, _ := Promote(skillDir, active)
	if res.Passed {
		t.Fatal("危險技能不該晉升")
	}
	// 原資料夾應仍在、active 不該有
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Error("把關不過時提案資料夾應保留原處")
	}
	if _, err := os.Stat(filepath.Join(active, "x")); !os.IsNotExist(err) {
		t.Error("把關不過時不該出現在 active")
	}
}
