package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
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
	applied, _, err := ApplyProposedMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(applied, "\n"), "pnpm") {
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
	if out, _, err := ApplyProposedMemory(root); err != nil || len(out) != 0 {
		t.Errorf("沒提案時應回空，got out=%q err=%v", out, err)
	}
}

func TestDiscardProposedMemory(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"learnings": ["x 慣例"]}`}
	_, _ = NewMemorySynthesizer(fp, root).Reflect(t.Context(), "t", nil)

	dropped, err := DiscardProposedMemory(root)
	if err != nil || len(dropped) == 0 {
		t.Fatalf("應丟棄既有提案，got dropped=%v err=%v", dropped, err)
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

// 逐條審核：先前是全有全無——一批裡夾一條爛的就得整批丟，而反思本來就是批次產出的。
func TestApplyProposedMemory_PerEntry(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"learnings": ["好的 A 慣例", "壞的 B 慣例", "好的 C 慣例"]}`}
	if _, err := NewMemorySynthesizer(fp, root).Reflect(t.Context(), "任務", nil); err != nil {
		t.Fatal(err)
	}

	all := ListProposedMemory(root)
	if len(all) != 3 || all[0].N != 1 || all[2].N != 3 {
		t.Fatalf("應解析出 3 條、編號 1..3，got %+v", all)
	}

	// 只放行 1 和 3
	applied, _, err := ApplyProposedMemory(root, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || !strings.Contains(applied[0], "A") || !strings.Contains(applied[1], "C") {
		t.Errorf("應只放行 A、C，got %v", applied)
	}

	// 未選中的 B 仍留在提案檔，且【重新編號為 1】
	rest := ListProposedMemory(root)
	if len(rest) != 1 || !strings.Contains(rest[0].Learning, "B") || rest[0].N != 1 {
		t.Fatalf("B 應留下並重新編號為 1，got %+v", rest)
	}
	// 分組標頭要保留（否則放行後的記錄失去 kind/task 溯源）
	if rest[0].Header == "" || rest[0].Kind == "" {
		t.Errorf("回寫應保留分組標頭，got header=%q kind=%q", rest[0].Header, rest[0].Kind)
	}

	// 記錄只落了 A、C
	memDir := filepath.Join(root, ".claw", "memory")
	entries, _ := os.ReadDir(memDir)
	if len(entries) != 2 {
		t.Errorf("應只有 2 筆記錄，got %d", len(entries))
	}

	// 丟棄剩下那條 → 提案檔消失
	dropped, err := DiscardProposedMemory(root, 1)
	if err != nil || len(dropped) != 1 {
		t.Fatalf("應丟棄 1 條，got %v err=%v", dropped, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claw", ProposedMemoryFileName)); !os.IsNotExist(err) {
		t.Error("全部處置完畢後提案檔應消失")
	}
}

// 編號不存在時不該動任何東西（避免「打錯字就把整批放行」）。
func TestApplyProposedMemory_UnknownIndexIsNoop(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"learnings": ["只有一條"]}`}
	_, _ = NewMemorySynthesizer(fp, root).Reflect(t.Context(), "t", nil)

	applied, _, err := ApplyProposedMemory(root, 9)
	if err != nil || len(applied) != 0 {
		t.Errorf("編號不存在應為 no-op，got %v err=%v", applied, err)
	}
	if len(ListProposedMemory(root)) != 1 {
		t.Error("no-op 後提案應原封不動")
	}
}

// 使用者偏好與專案慣例分流：前者標成 UserProfileTag，放行後正文每輪常駐（不必等 recall）。
func TestMemoryReflect_RoutesUserFacts(t *testing.T) {
	root := t.TempDir()
	fp := &fakeProvider{content: `{"learnings": ["本專案用 pnpm 而非 npm"], "user_facts": ["使用者要繁體中文回覆"]}`}
	m := NewMemorySynthesizer(fp, root)

	added, err := m.Reflect(t.Context(), "裝依賴並跑測試", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 {
		t.Fatalf("慣例 + 使用者事實共 2 條，got %v", added)
	}

	entries := ListProposedMemory(root)
	kinds := map[string]string{}
	for _, e := range entries {
		kinds[e.Kind] = e.Learning
	}
	if kinds["慣例"] != "本專案用 pnpm 而非 npm" {
		t.Errorf("慣例分流錯了: %+v", entries)
	}
	if kinds[ctxpkg.UserProfileTag] != "使用者要繁體中文回覆" {
		t.Errorf("使用者事實應標成 %q: %+v", ctxpkg.UserProfileTag, entries)
	}

	// 放行後落地的記錄要帶 tags: [user]，否則 LoadIndex 認不出來、常駐待遇形同虛設。
	if _, _, err := ApplyProposedMemory(root); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(root, ".claw", "memory", "mem-*.md"))
	found := false
	for _, f := range files {
		if b, _ := os.ReadFile(f); strings.Contains(string(b), "tags: ["+ctxpkg.UserProfileTag+"]") {
			found = true
		}
	}
	if !found {
		t.Errorf("放行後應有一筆 tags: [%s] 的記錄，got %v", ctxpkg.UserProfileTag, files)
	}
}

// 自動放行只碰【新增】：破壞性提案（UPDATE/DELETE）必須原封不動留在提案檔等人審。
// auto-apply 的整條安全論證都繫在這裡——新增的爆炸半徑是一個檔，刪改不是。
func TestAutoApplyAdditions_SkipsDestructive(t *testing.T) {
	root := t.TempDir()
	proposed := filepath.Join(root, ".claw", ProposedMemoryFileName)
	if err := os.MkdirAll(filepath.Dir(proposed), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `## [整併] 2026-08-05T14:30:00+08:00
- UPDATE mem-1a2b3c4d — 本專案用 pnpm；CI 也是
  舊：本專案用 pnpm 而非 npm 裝依賴
  因：新事實推翻了原本的暗示
- DELETE mem-5e6f7a8b
  值：Node 14 需要 --experimental-modules
  因：專案已升到 Node 22
- 部署前先跑 make verify
`
	if err := os.WriteFile(proposed, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	// 未啟用：什麼都不該動。
	t.Setenv(EnvAutoApply, "")
	if applied, err := AutoApplyAdditions(root); err != nil || applied != nil {
		t.Fatalf("未啟用時不該放行任何東西，got %v, %v", applied, err)
	}

	t.Setenv(EnvAutoApply, "1")
	applied, err := AutoApplyAdditions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "部署前先跑 make verify" {
		t.Fatalf("只該放行那條新增，got %v", applied)
	}

	if files, _ := filepath.Glob(filepath.Join(root, ".claw", "memory", "mem-*.md")); len(files) != 1 {
		t.Fatalf("應寫出 1 筆記憶記錄，got %v", files)
	}

	// 破壞性的兩條原封不動留著；已放行的新增要從提案檔消失。
	rest := readFileIgnore(proposed)
	for _, want := range []string{"UPDATE mem-1a2b3c4d", "DELETE mem-5e6f7a8b"} {
		if !strings.Contains(rest, want) {
			t.Errorf("破壞性提案不該被自動放行，提案檔已無 %q：\n%s", want, rest)
		}
	}
	if strings.Contains(rest, "部署前先跑 make verify") {
		t.Errorf("已放行的新增不該留在提案檔：\n%s", rest)
	}
}
