package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 多租戶：COGITO_MEMORY_SCOPE=channel 的地基——技能與記憶可【獨立 rooted】。
// 這是「記憶 per-conversation、技能仍共享」的必要機制（見 docs/multi-tenancy.md）。
func writeRecord(t *testing.T, root, sub, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, ".claw", sub)
	if sub == "skills" {
		dir = filepath.Join(dir, name) // 技能是 folder-per-skill：.claw/skills/<name>/SKILL.md
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fname := name + ".md"
	if sub == "skills" {
		fname = "SKILL.md"
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n內容\n"
	if err := os.WriteFile(filepath.Join(dir, fname), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPromptComposer_MemoryRootIndependentOfSkills(t *testing.T) {
	skillsDir := t.TempDir()
	memA := t.TempDir()
	memB := t.TempDir() // 空：模擬另一個對話尚無記憶
	writeRecord(t, skillsDir, "skills", "foo-skill", "共享技能")
	writeRecord(t, memA, "memory", "alpha-secret", "對話A的機密記憶")

	// 技能 root=skillsDir、記憶 root=memA：兩者都該出現（且各自來源正確）
	out := string(NewPromptComposer(skillsDir, memA, false).Build().Content)
	if !strings.Contains(out, "foo-skill") {
		t.Error("技能索引應含 skillsDir 的 foo-skill")
	}
	if !strings.Contains(out, "alpha-secret") {
		t.Error("記憶索引應含 memA 的 alpha-secret")
	}

	// 換記憶 root=memB(空)：技能仍在（共享），記憶不見（per-conversation 隔離）
	out2 := string(NewPromptComposer(skillsDir, memB, false).Build().Content)
	if !strings.Contains(out2, "foo-skill") {
		t.Error("換記憶 root 後技能仍應共享可見")
	}
	if strings.Contains(out2, "alpha-secret") {
		t.Error("記憶 root=memB(空) 不該看到 memA 的機密記憶——這正是跨對話隔離")
	}

	// memoryDir="" → 回退到技能 workDir（現況、單一目錄場景行為不變）
	out3 := string(NewPromptComposer(memA, "", false).Build().Content)
	if !strings.Contains(out3, "alpha-secret") {
		t.Error("memoryDir 空時應回退到 workDir 讀記憶")
	}
}

// recall 讀路徑的隔離（recall 工具內部即 MemoryLoader）：A 的記錄在 A 的 loader 讀得到、B 讀不到。
func TestMemoryLoader_PerRootIsolation(t *testing.T) {
	memA := t.TempDir()
	memB := t.TempDir()
	writeRecord(t, memA, "memory", "alpha-secret", "對話A的機密")

	if got := len(NewMemoryLoader(memA).Records()); got != 1 {
		t.Errorf("memA 應有 1 筆記憶，得到 %d", got)
	}
	if got := len(NewMemoryLoader(memB).Records()); got != 0 {
		t.Errorf("memB 不該看到 memA 的記憶，得到 %d 筆", got)
	}
}
