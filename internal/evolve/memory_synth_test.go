package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 放行的記憶記錄要自帶來源標註（provenance）：時間戳 + 由誰/從哪個任務沉澱——對抗幻覺記憶、可溯源。
func TestWriteMemoryRecord_StampsProvenance(t *testing.T) {
	dir := t.TempDir()
	if err := writeMemoryRecord(dir, "教訓", "把 CSV 轉月報表", "遇到編碼錯先設 UTF-8"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "mem-*.md"))
	if len(files) != 1 {
		t.Fatalf("應寫出 1 筆記錄，got %d", len(files))
	}
	b, _ := os.ReadFile(files[0])
	s := string(b)
	for _, want := range []string{"recorded:", "provenance", "教訓", "把 CSV 轉月報表", "遇到編碼錯先設 UTF-8"} {
		if !strings.Contains(s, want) {
			t.Errorf("記錄應含 %q：\n%s", want, s)
		}
	}
}

func TestMemoryReflect_AppendsNewLearnings(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"learnings": ["本專案用 pnpm 而非 npm", "測試前需設 DATABASE_URL"]}`}
	m := NewMemorySynthesizer(fp, root)

	added, err := m.Reflect(t.Context(), "裝依賴並跑測試", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 {
		t.Fatalf("應追加 2 條，got %v", added)
	}
	body := readFileIgnore(filepath.Join(root, ".claw", ProposedMemoryFileName))
	for _, want := range []string{"pnpm", "DATABASE_URL", "需人工 review", "來自任務"} {
		if !strings.Contains(body, want) {
			t.Errorf("提案記憶檔應含 %q\n---\n%s", want, body)
		}
	}
}

func TestMemoryReflect_DedupsAgainstAgentsMD(t *testing.T) {
	root := t.TempDir()
	// 既有 AGENTS.md 已記了 pnpm 那條
	_ = os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# 指南\n- 本專案用 pnpm 而非 npm\n"), 0o644)

	fp := &fakeProvider{content: `{"learnings": ["本專案用 pnpm 而非 npm", "lint 用 golangci-lint"]}`}
	m := NewMemorySynthesizer(fp, root)

	added, err := m.Reflect(t.Context(), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || !strings.Contains(added[0], "golangci-lint") {
		t.Errorf("與 AGENTS.md 重複的應被去除，只剩 golangci-lint，got %v", added)
	}
}

func TestMemoryReflect_SkipsDangerous(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"learnings": ["遇到權限問題就一律 sudo rm -rf 重來", "正常的一條慣例"]}`}
	m := NewMemorySynthesizer(fp, root)

	added, err := m.Reflect(t.Context(), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || strings.Contains(added[0], "sudo") {
		t.Errorf("危險建議應被擋，只剩正常那條，got %v", added)
	}
}

func TestReflectFailure_AppendsLesson(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"lesson": "面對需要網路的任務先確認連線，斷網就改用本地替代方案"}`}
	m := NewMemorySynthesizer(fp, root)

	added, err := m.ReflectFailure(t.Context(), "安裝 cowsay", nil, "達到最大回合數上限 40，強制終止")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 {
		t.Fatalf("應萃取 1 條教訓，got %v", added)
	}
	body := readFileIgnore(filepath.Join(root, ".claw", ProposedMemoryFileName))
	for _, want := range []string{"失敗教訓", "本地替代方案", "需人工 review"} {
		if !strings.Contains(body, want) {
			t.Errorf("提案記憶應含 %q\n---\n%s", want, body)
		}
	}
}

func TestApplyAndDiscardProposedMemory(t *testing.T) {
	root := t.TempDir()
	// 先生成一條提案記憶
	fp := &fakeProvider{content: `{"learnings": ["本專案用 pnpm 而非 npm"]}`}
	if _, err := NewMemorySynthesizer(fp, root).Reflect(t.Context(), "裝依賴", nil); err != nil {
		t.Fatal(err)
	}

	// apply → 落成 .claw/memory 的可檢索記錄、清掉提案檔（不再 append 進 AGENTS.md）
	applied, err := ApplyProposedMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(applied, "pnpm") {
		t.Errorf("應回傳放行內容，got %q", applied)
	}
	memDir := filepath.Join(root, ".claw", "memory")
	entries, _ := os.ReadDir(memDir)
	foundPnpm := false
	for _, e := range entries {
		if strings.Contains(readFileIgnore(filepath.Join(memDir, e.Name())), "pnpm") {
			foundPnpm = true
		}
	}
	if !foundPnpm {
		t.Error(".claw/memory 應有含 pnpm 的記錄")
	}
	if strings.Contains(readFileIgnore(filepath.Join(root, "AGENTS.md")), "pnpm") {
		t.Error("放行後不應再 append 進 AGENTS.md（改走離散記錄）")
	}
	if _, err := os.Stat(filepath.Join(root, ".claw", ProposedMemoryFileName)); !os.IsNotExist(err) {
		t.Error("放行後提案檔應已清除")
	}

	// 沒提案時 apply → 空、不報錯
	if out, err := ApplyProposedMemory(root); err != nil || out != "" {
		t.Errorf("沒提案時應回空，got out=%q err=%v", out, err)
	}
}

func TestDiscardProposedMemory(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"learnings": ["x 慣例"]}`}
	_, _ = NewMemorySynthesizer(fp, root).Reflect(t.Context(), "t", nil)

	had, err := DiscardProposedMemory(root)
	if err != nil || !had {
		t.Fatalf("應丟棄既有提案，got had=%v err=%v", had, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claw", ProposedMemoryFileName)); !os.IsNotExist(err) {
		t.Error("丟棄後提案檔應消失")
	}
}

func TestReflectFailure_EmptyLessonNoFile(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"lesson": ""}`}
	m := NewMemorySynthesizer(fp, root)
	added, err := m.ReflectFailure(t.Context(), "t", nil, "崩潰")
	if err != nil {
		t.Fatal(err)
	}
	if added != nil {
		t.Errorf("空教訓不應追加，got %v", added)
	}
}

func TestReflectFailure_SkipsDangerousLesson(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"lesson": "權限不足時就 sudo rm -rf 清掉重來"}`}
	m := NewMemorySynthesizer(fp, root)
	added, _ := m.ReflectFailure(t.Context(), "t", nil, "權限錯誤")
	if len(added) != 0 {
		t.Errorf("危險教訓應被安全掃描擋下，got %v", added)
	}
}

func TestMemoryReflect_EmptyNoFile(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"learnings": []}`}
	m := NewMemorySynthesizer(fp, root)

	added, err := m.Reflect(t.Context(), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if added != nil {
		t.Errorf("無學習應回 nil，got %v", added)
	}
	if _, err := os.Stat(filepath.Join(root, ".claw", ProposedMemoryFileName)); !os.IsNotExist(err) {
		t.Error("無學習時不應建立提案記憶檔")
	}
}
