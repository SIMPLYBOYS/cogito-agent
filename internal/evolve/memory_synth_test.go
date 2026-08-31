package evolve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

// 放行的記憶記錄要自帶來源標註（provenance）：時間戳 + 由誰/從哪個任務沉澱——對抗幻覺記憶、可溯源。
func TestWriteMemoryRecord_StampsProvenance(t *testing.T) {
	dir := t.TempDir()
	if err := writeMemoryRecord(dir, "教訓", "把 CSV 轉月報表", "遇到編碼錯先設 UTF-8", ""); err != nil {
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
// 自動放行只吃「專案慣例」，不吃「使用者畫像」。
//
// 這條線是掃過 123 條實際記憶之後畫出來的：慣例類品質普遍好（能從軌跡驗證），
// user 類卻是在猜「這個人是什麼樣的人」而樣本只有幾句一次性指令——於是「為了省錢
// 說的那句『不開會不上板』」變成了他的偏好，跟另外六條「開工前一定要看板子」打架。
// 錯的專案事實下次讀程式碼會被推翻；錯的人物畫像不會。
func TestAutoApplyAdditions_KeepsUserProfileForReview(t *testing.T) {
	root := t.TempDir()
	proposed := filepath.Join(root, ".claw", ProposedMemoryFileName)
	if err := os.MkdirAll(filepath.Dir(proposed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proposed, []byte(`## [慣例] 來自任務「A」（ts）
- 部署前先跑 make verify

## [`+ctxpkg.UserProfileTag+`] 來自任務「A」（ts）
- 使用者偏好直接派工、不開會不上板
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAutoApply, "1")
	applied, err := AutoApplyAdditions(root, passAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !strings.Contains(applied[0], "make verify") {
		t.Fatalf("只該自動放行慣例類，got %v", applied)
	}
	rest := readFileIgnore(proposed)
	if !strings.Contains(rest, "不開會不上板") {
		t.Errorf("使用者畫像該留在提案檔等人過目：\n%s", rest)
	}
	if n := PendingProposals(root); n != 1 {
		t.Errorf("待放行條數應為 1（通知措辭靠它才不會謊稱全部生效），got %d", n)
	}
}

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
	if applied, err := AutoApplyAdditions(root, passAll); err != nil || applied != nil {
		t.Fatalf("未啟用時不該放行任何東西，got %v, %v", applied, err)
	}

	t.Setenv(EnvAutoApply, "1")
	applied, err := AutoApplyAdditions(root, passAll)
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

// 呈現提案前先跑衝突偵測：一條一條審的真正成本不在判斷，在【搜尋】——157 條的時候
// 「有沒有相關的」根本想不起來，於是提案就堆著不審。
func TestConflictHits(t *testing.T) {
	root := t.TempDir()
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(slug, desc string) {
		body := "---\nname: " + slug + "\ndescription: " + desc + "\n---\n" + desc
		if err := os.WriteFile(filepath.Join(memDir, slug+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("mem-pnpm", "本專案用 pnpm 而非 npm 裝依賴")
	write("mem-port", "起本地 server 前先查埠是否被占")

	hits := ConflictHits(root, ProposedMemoryEntry{Op: OpAdd, Learning: "裝依賴一律用 pnpm，不要用 npm"})
	if len(hits) == 0 || hits[0].Name != "mem-pnpm" {
		t.Fatalf("該標出 pnpm 那條，got %+v", hits)
	}

	// UPDATE/DELETE 不必猜：它們指名了 Target，那條就是受影響的條目
	hits = ConflictHits(root, ProposedMemoryEntry{Op: OpUpdate, Target: "mem-port", Learning: "隨便"})
	if len(hits) != 1 || hits[0].Name != "mem-port" {
		t.Fatalf("指名 Target 時該直接回那一條，got %+v", hits)
	}

	// ⚠ 掃描【不是】使用：記進帳本會把一大批記憶標成「剛用過」，
	// 而索引的 LRU 排序與 Prune 的淘汰都靠那個訊號——污染了會讓冷門記憶賴著不走。
	if _, err := os.Stat(filepath.Join(root, ".claw", "memory-usage.json")); err == nil {
		u, _ := os.ReadFile(filepath.Join(root, ".claw", "memory-usage.json"))
		if strings.Contains(string(u), `"hits": 1`) {
			t.Error("衝突偵測記帳了——那會污染索引排序與淘汰決策")
		}
	}
}

// 反思要看得到【既有記憶】，否則每輪都在真空裡想事情。
// 實測：31 條待審提案裡有 27 條落在重複叢集——同一個意思用不同的詞寫了九遍。
// 字面去重擋不住（那 27 條裡只有 6 條字面夠像），得讓模型自己讀懂「這是同一件事」。
func TestReflect_PromptCarriesExistingMemory(t *testing.T) {
	root := t.TempDir()
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: m1\ndescription: 派三人並行評審再收斂\n---\n正文"
	if err := os.WriteFile(filepath.Join(memDir, "mem-1.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := &capturingProvider{content: `{"learnings": [], "user_facts": []}`}
	if _, err := NewMemorySynthesizer(cp, root).Reflect(context.Background(), "任務", nil); err != nil {
		t.Fatal(err)
	}
	sent := cp.msgs[len(cp.msgs)-1].Content
	if !strings.Contains(sent, "派三人並行評審再收斂") {
		t.Errorf("既有記憶沒進 prompt：\n%s", sent)
	}
	// 放 user 不放 system：記憶庫每次都不一樣，塞進 system 那段就再也快取不到
	if strings.Contains(cp.msgs[0].Content, "派三人並行評審再收斂") {
		t.Error("既有記憶跑進 system prompt 了，會打掉快取")
	}
}

// name 是知識圖譜的節點 ID 兼 [[link]] 目標，所以不能硬切在標點/標記中間。
//
// 實測背景：正式記憶庫 14 個節點、0 條邊，因為 name 全是斷句
// （「外部工具（MCP/API）查不到或未掛載時，**」——沒有人寫得出指向它的連結）。
// 見 docs/kg-status.md §4。
func TestMemoryTitle_CutsAtClauseBoundary(t *testing.T) {
	cases := []struct {
		name     string
		learning string
		reject   string // 標題結尾不該出現的殘渣
	}{
		{"斷在粗體標記", "外部工具（MCP/API）查不到或未掛載時，**先確認工具名稱是否正確**", "*"},
		{"斷在開引號", "涉及數值區間的過濾（如「年薪≥140萬」遇到「130~180萬」的區間職缺）該取下限", "「"},
		{"斷在逗號", "處理地址或路名查詢時，先用 GROUP BY 行政區彙總，再逐筆比對門牌", "，"},
		{"未閉合的括號", "涉及數值區間的過濾（如「年薪≥140萬」遇到「130~180萬」的職缺）取下限", "遇到"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := memoryTitle(c.learning)
			if got == "" {
				t.Fatal("標題不該是空的")
			}
			if len([]rune(got)) > memoryTitleRunes {
				t.Errorf("標題超過上限 %d：%q", memoryTitleRunes, got)
			}
			if strings.HasSuffix(got, c.reject) {
				t.Errorf("標題結尾殘留 %q（切在標記/標點中間，當不了連結目標）：%q", c.reject, got)
			}
		})
	}
}

// 短學習原樣保留，不該被動到。
func TestMemoryTitle_ShortLearningUnchanged(t *testing.T) {
	const short = "查開放資料前先取一筆看欄位"
	if got := memoryTitle(short); got != short {
		t.Errorf("短學習不該被改寫：%q → %q", short, got)
	}
}

// trigger 的完整旅程：反思尾綴 → 提案 bullet 續行 → 解析 → 放行 → 記錄 frontmatter。
//
// 五個接點（Cut 拆綴、renderProposedBullet、attachMeta、ApplyProposedMemory、
// writeMemoryRecord）任何一個掉了，觸發詞就靜默消失——檢索照常運作，只是永遠比對不到
// 那一欄，跟沒做過一樣。這正是「資料量到了卻救不回來」型缺陷（同 LatencyMS 那課）。
func TestTriggerRoundTrip_ReflectionToRecord(t *testing.T) {
	root := t.TempDir()
	m := NewMemorySynthesizer(nil, root) // provider 不用：直接餵 proposeLearnings

	added, err := m.proposeLearnings("查台北房價",
		[]string{"實價登錄單價為每平方公尺，換算每坪乘 3.305785｜觸發：房價 坪數 每坪"}, "慣例")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || strings.Contains(added[0], "觸發") {
		t.Fatalf("回傳的事實清單不該殘留觸發尾綴：%q", added)
	}

	// 提案檔要能被解析回 Trigger（人審與逐條放行都靠這條路）
	entries := ListProposedMemory(root)
	if len(entries) != 1 {
		t.Fatalf("應有 1 條提案，實際 %d", len(entries))
	}
	if entries[0].Trigger != "房價 坪數 每坪" {
		t.Fatalf("提案解析丟了觸發詞：%+v", entries[0])
	}

	// 放行 → 記錄 frontmatter 帶 trigger:，且 context 端解析得出來
	if _, _, err := ApplyProposedMemory(root); err != nil {
		t.Fatal(err)
	}
	recs := ctxpkg.NewMemoryLoader(root).Records()
	if len(recs) != 1 {
		t.Fatalf("應有 1 筆記錄，實際 %d", len(recs))
	}
	if recs[0].Trigger != "房價 坪數 每坪" {
		t.Errorf("落盤的記錄缺 trigger（frontmatter 沒寫或沒解析）：%+v", recs[0])
	}
}
