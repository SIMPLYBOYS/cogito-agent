package evolve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// 最要緊的一條：舊格式（純 bullet）的解析結果一個位元都不能變。整併是加法，不是改法。
func TestParseProposedMemory_BackwardCompatible(t *testing.T) {
	raw := `<!-- ⚠️ 自動生成 -->

## [慣例] 來自任務「裝依賴」（2026-08-05T10:00:00+08:00）
- 本專案用 pnpm 而非 npm 裝依賴
- 測試前需設 DATABASE_URL

## [失敗教訓] 來自任務「跑測試」（2026-08-05T11:00:00+08:00）
- 埠被占時先 lsof 再起
`
	got := parseProposedMemory(raw)
	if len(got) != 3 {
		t.Fatalf("應解出 3 條，got %d", len(got))
	}
	for _, e := range got {
		if e.Op != OpAdd {
			t.Errorf("#%d 舊格式應為 add，got %q", e.N, e.Op)
		}
		if e.IsDestructive() {
			t.Errorf("#%d 舊格式不該是破壞性", e.N)
		}
		if e.Target != "" || e.Old != "" || e.Why != "" {
			t.Errorf("#%d 舊格式不該有動作欄位: %+v", e.N, e)
		}
	}
	if got[0].Learning != "本專案用 pnpm 而非 npm 裝依賴" || got[0].Kind != "慣例" {
		t.Errorf("第一條內容/分類錯: %+v", got[0])
	}
	if got[2].Kind != "失敗教訓" || got[2].N != 3 {
		t.Errorf("第三條分組/編號錯: %+v", got[2])
	}
}

func TestParseProposedMemory_Actions(t *testing.T) {
	raw := `## [整併] 2026-08-05T14:30:00+08:00
- UPDATE mem-1a2b3c4d — 本專案用 pnpm；CI 也是
  舊：本專案用 pnpm 而非 npm 裝依賴
  因：新事實推翻了原本的暗示
- DELETE mem-5e6f7a8b
  值：Node 14 需要 --experimental-modules
  因：專案已升到 Node 22
- 部署前先跑 make verify
`
	got := parseProposedMemory(raw)
	if len(got) != 3 {
		t.Fatalf("應解出 3 條，got %d", len(got))
	}

	u := got[0]
	if u.Op != OpUpdate || u.Target != "mem-1a2b3c4d" {
		t.Errorf("UPDATE 解析錯: %+v", u)
	}
	if u.Learning != "本專案用 pnpm；CI 也是" {
		t.Errorf("UPDATE 新值錯: %q", u.Learning)
	}
	if u.Old != "本專案用 pnpm 而非 npm 裝依賴" || u.Why != "新事實推翻了原本的暗示" {
		t.Errorf("UPDATE 附帶行錯: old=%q why=%q", u.Old, u.Why)
	}

	d := got[1]
	if d.Op != OpDelete || d.Target != "mem-5e6f7a8b" {
		t.Errorf("DELETE 解析錯: %+v", d)
	}
	if d.Old != "Node 14 需要 --experimental-modules" || d.Why == "" {
		t.Errorf("DELETE 附帶行錯: %+v", d)
	}

	// 同一區塊裡混著純 ADD 仍要正常
	if got[2].Op != OpAdd || got[2].Learning != "部署前先跑 make verify" {
		t.Errorf("混排的 ADD 錯: %+v", got[2])
	}
	// 編號連續、不因附帶行位移
	for i, e := range got {
		if e.N != i+1 {
			t.Errorf("編號位移：第 %d 條 N=%d", i, e.N)
		}
	}
}

// 分隔符要寬鬆：我們寫「— 」，模型可能吐 "-" 或 ":"。
func TestParseAction_SeparatorTolerance(t *testing.T) {
	for _, body := range []string{
		"UPDATE mem-x — 新值",
		"UPDATE mem-x - 新值",
		"UPDATE mem-x: 新值",
		"UPDATE mem-x 新值",
	} {
		var e ProposedMemoryEntry
		parseAction(&e, body)
		if e.Op != OpUpdate || e.Target != "mem-x" || e.Learning != "新值" {
			t.Errorf("%q → %+v", body, e)
		}
	}
}

// 殘缺的動作不能被丟掉——丟掉會讓編號位移，使用者看到的清單與檔案對不上。
// 留著讓放行路徑報明確原因。
func TestParseProposedMemory_MalformedActionKeepsNumbering(t *testing.T) {
	raw := `## [整併] ts
- UPDATE mem-x
- 正常的一條
`
	got := parseProposedMemory(raw)
	if len(got) != 2 {
		t.Fatalf("殘缺動作不該被丟棄，應有 2 條，got %d", len(got))
	}
	if got[0].Op != OpUpdate || got[0].Learning != "" {
		t.Errorf("殘缺 UPDATE 應保留且新值為空: %+v", got[0])
	}
	if got[1].N != 2 {
		t.Errorf("編號位移了: %+v", got[1])
	}
}

// 純 ADD 底下碰巧的縮排文字不該被誤讀成欄位（舊檔可能有）。
func TestParseProposedMemory_IndentUnderAddIgnored(t *testing.T) {
	raw := `## [慣例] ts
- 一般事實
  因：這行不該被吃進去
`
	got := parseProposedMemory(raw)
	if len(got) != 1 || got[0].Why != "" {
		t.Errorf("ADD 底下的縮排應忽略: %+v", got)
	}
}

// round-trip：逐條放行時未選中的條目原樣寫回。少了這層，一條 UPDATE 被跳過一次就會
// 退化成純文字 ADD，下次放行等於憑空多一筆記憶。
func TestRenderProposedBullet_RoundTrip(t *testing.T) {
	orig := `## [整併] 2026-08-05T14:30:00+08:00
- UPDATE mem-1a2b3c4d — 本專案用 pnpm；CI 也是
  舊：本專案用 pnpm 而非 npm 裝依賴
  因：新事實推翻了原本的暗示
- DELETE mem-5e6f7a8b
  值：Node 14 需要 --experimental-modules
  因：專案已升到 Node 22
- 部署前先跑 make verify
`
	first := parseProposedMemory(orig)

	var b strings.Builder
	b.WriteString(first[0].Header + "\n")
	for _, e := range first {
		b.WriteString(renderProposedBullet(e))
	}
	second := parseProposedMemory(b.String())

	if len(second) != len(first) {
		t.Fatalf("round-trip 條數不符: %d → %d", len(first), len(second))
	}
	for i := range first {
		a, c := first[i], second[i]
		if a.Op != c.Op || a.Target != c.Target || a.Learning != c.Learning ||
			a.Old != c.Old || a.Why != c.Why {
			t.Errorf("第 %d 條 round-trip 失真:\n  前 %+v\n  後 %+v", i+1, a, c)
		}
	}
}

// slug 內含 "-"（mem-1a2b3c4d），剝分隔符時不能咬到它。
func TestParseAction_SlugKeepsHyphen(t *testing.T) {
	for _, body := range []string{
		"UPDATE mem-1a2b3c4d — 新值",
		"UPDATE mem-1a2b3c4d: 新值",
		"DELETE mem-1a2b3c4d",
	} {
		var e ProposedMemoryEntry
		parseAction(&e, body)
		if e.Target != "mem-1a2b3c4d" {
			t.Errorf("%q → Target=%q（slug 被咬掉了）", body, e.Target)
		}
	}
}

// seedRecords 在 <root>/.claw/memory 造記錄。tags 空＝不加 tags 行。
func seedRecords(t *testing.T, root string, specs ...[2]string) {
	t.Helper()
	dir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, sp := range specs {
		desc, tags := sp[0], sp[1]
		tagLine := ""
		if tags != "" {
			tagLine = "tags: [" + tags + "]\n"
		}
		body := fmt.Sprintf("---\nname: r%02d\ndescription: %s\n%s---\n%s\n", i, desc, tagLine, desc)
		// 檔名決定編號順序（List 依 Path 排序）
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("mem-%02d.md", i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReconcile_WritesDiffableProposal(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root,
		[2]string{"本專案用 npm 裝依賴", "慣例"},
		[2]string{"本專案改用 pnpm 裝依賴", "慣例"})

	fp := &fakeProvider{content: `{"actions":[
	  {"op":"update","n":1,"fact":"本專案用 pnpm 裝依賴（2026-07 起）","why":"第 2 條已推翻第 1 條"}
	]}`}
	got, err := NewMemorySynthesizer(fp, root).Reconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("應產出 1 條提案，got %v", got)
	}

	entries := ListProposedMemory(root)
	if len(entries) != 1 {
		t.Fatalf("提案檔應有 1 條，got %d", len(entries))
	}
	e := entries[0]
	if e.Op != OpUpdate || e.Target != "mem-00" {
		t.Errorf("動作/目標錯: %+v", e)
	}
	// 人審要看得到 diff：舊值、新值、理由三者缺一不可
	if e.Old != "本專案用 npm 裝依賴" || e.Learning == "" || e.Why == "" {
		t.Errorf("提案缺 diff 資訊: old=%q new=%q why=%q", e.Old, e.Learning, e.Why)
	}
}

// 護欄①（提案時）：tags:[user] 的記錄不可被提案刪除，但可被提案修改。
func TestReconcile_UserProfileNotDeletable(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root,
		[2]string{"使用者要繁體中文回覆", ctxpkg.UserProfileTag},
		[2]string{"某條可刪的慣例", "慣例"})

	fp := &fakeProvider{content: `{"actions":[
	  {"op":"delete","n":1,"why":"我覺得不需要了"},
	  {"op":"delete","n":2,"why":"確實過時"}
	]}`}
	if _, err := NewMemorySynthesizer(fp, root).Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries := ListProposedMemory(root)
	if len(entries) != 1 {
		t.Fatalf("畫像那條應被擋掉，只剩 1 條，got %d: %+v", len(entries), entries)
	}
	if entries[0].Target != "mem-01" {
		t.Errorf("擋錯條了: %+v", entries[0])
	}

	// UPDATE 畫像則放行（偏好會變，但要人看 diff）
	root2 := t.TempDir()
	seedRecords(t, root2, [2]string{"使用者要繁體中文", ctxpkg.UserProfileTag}, [2]string{"x", "慣例"})
	fp2 := &fakeProvider{content: `{"actions":[{"op":"update","n":1,"fact":"使用者要繁體中文，且不要簡體","why":"使用者補充"}]}`}
	if _, err := NewMemorySynthesizer(fp2, root2).Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e := ListProposedMemory(root2); len(e) != 1 || e[0].Op != OpUpdate {
		t.Errorf("畫像應可被 UPDATE 提案: %+v", e)
	}
}

func TestReconcile_RejectsBadActions(t *testing.T) {
	cases := []struct{ name, resp string }{
		{"編號越界", `{"actions":[{"op":"delete","n":99,"why":"x"}]}`},
		{"沒有理由", `{"actions":[{"op":"delete","n":1}]}`},
		{"未知動作", `{"actions":[{"op":"merge","n":1,"why":"x"}]}`},
		{"改了等於沒改", `{"actions":[{"op":"update","n":1,"fact":"事實 A","why":"x"}]}`},
		{"危險內容", `{"actions":[{"op":"add","fact":"部署前先跑 sudo rm -rf / 清乾淨"}]}`},
	}
	for _, c := range cases {
		root := t.TempDir()
		seedRecords(t, root, [2]string{"事實 A", "慣例"}, [2]string{"事實 B", "慣例"})
		got, err := NewMemorySynthesizer(&fakeProvider{content: c.resp}, root).Reconcile(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != 0 || len(ListProposedMemory(root)) != 0 {
			t.Errorf("%s: 應被擋下，卻產出 %v", c.name, got)
		}
	}
}

// 解析失敗整批不動，與既有 evolve 管線一致。
func TestReconcile_BadJSONAbortsBatch(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, [2]string{"A", "慣例"}, [2]string{"B", "慣例"})
	_, err := NewMemorySynthesizer(&fakeProvider{content: "我覺得不用改"}, root).Reconcile(t.Context())
	if err == nil {
		t.Error("非 JSON 應回錯")
	}
	if len(ListProposedMemory(root)) != 0 {
		t.Error("解析失敗不該寫入任何提案")
	}
}

// 整併提案與 Reflect 的產物共用同一份檔案與同一套編號——memory list / apply 完全不必知道有整併。
func TestReconcile_SharesNumberingWithReflect(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, [2]string{"舊事實", "慣例"}, [2]string{"新事實", "慣例"})

	addFP := &fakeProvider{content: `{"learnings":["一條普通慣例"],"user_facts":[]}`}
	if _, err := NewMemorySynthesizer(addFP, root).Reflect(t.Context(), "某任務", nil); err != nil {
		t.Fatal(err)
	}
	recFP := &fakeProvider{content: `{"actions":[{"op":"delete","n":1,"why":"已被第 2 條取代"}]}`}
	if _, err := NewMemorySynthesizer(recFP, root).Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries := ListProposedMemory(root)
	if len(entries) != 2 {
		t.Fatalf("兩種來源應在同一份清單，got %d: %+v", len(entries), entries)
	}
	if entries[0].N != 1 || entries[0].Op != OpAdd || entries[1].N != 2 || entries[1].Op != OpDelete {
		t.Errorf("編號/順序錯: %+v", entries)
	}
}

// readRecord 讀出某 slug 的 description（護欄驗證用）。
func readRecord(t *testing.T, root, slug string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".claw", "memory", slug+".md"))
	if err != nil {
		t.Fatalf("讀 %s: %v", slug, err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "description:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "description:"))
		}
	}
	return ""
}

func writeProposal(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".claw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ProposedMemoryFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApply_UpdateRewritesInPlace(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, [2]string{"本專案用 npm", "慣例"})
	writeProposal(t, root, `## [整併] ts
- UPDATE mem-00 — 本專案改用 pnpm
  舊：本專案用 npm
  因：已切換
`)
	applied, skipped, err := ApplyProposedMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || len(skipped) != 0 {
		t.Fatalf("應套用 1 條: applied=%v skipped=%v", applied, skipped)
	}
	if got := readRecord(t, root, "mem-00"); got != "本專案改用 pnpm" {
		t.Errorf("內容沒改寫: %q", got)
	}
	// 檔名不變是刻意的——改名會讓 memory-usage.json 的使用歷史孤兒化
	if _, err := os.Stat(filepath.Join(root, ".claw", "memory", "mem-00.md")); err != nil {
		t.Errorf("檔名不該變: %v", err)
	}
}

// 護欄③：DELETE 是歸檔不是刪除。
func TestApply_DeleteArchivesNotRemoves(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, [2]string{"過時的事實", "慣例"})
	writeProposal(t, root, `## [整併] ts
- DELETE mem-00
  值：過時的事實
  因：已不適用
`)
	applied, skipped, err := ApplyProposedMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || len(skipped) != 0 {
		t.Fatalf("applied=%v skipped=%v", applied, skipped)
	}
	if _, err := os.Stat(filepath.Join(root, ".claw", "memory", "mem-00.md")); !os.IsNotExist(err) {
		t.Error("記錄應已移出 memory/")
	}
	if _, err := os.Stat(filepath.Join(root, ".claw", "memory-archive", "mem-00.md")); err != nil {
		t.Errorf("應歸檔到 memory-archive（可復原）: %v", err)
	}
}

// 放行時的三道護欄。提案檔是純文字、人可以手改，所以真正動檔案的這步必須自己再驗一次。
func TestApply_DestructiveGuards(t *testing.T) {
	cases := []struct {
		name, tags, proposal, wantNote string
	}{
		{
			name: "畫像不可刪", tags: ctxpkg.UserProfileTag,
			proposal: "- DELETE mem-00\n  值：使用者要繁體中文\n  因：手改混進來的\n",
			wantNote: "使用者畫像記錄不可刪除",
		},
		{
			name: "舊值對不上（樂觀鎖）", tags: "慣例",
			proposal: "- UPDATE mem-00 — 新值\n  舊：這不是目前的內容\n  因：x\n",
			wantNote: "記錄內容已變動",
		},
		{
			name: "目標不存在", tags: "慣例",
			proposal: "- DELETE mem-nonexistent\n  值：x\n  因：y\n",
			wantNote: "記錄已不存在",
		},
		{
			name: "提案殘缺（缺新值）", tags: "慣例",
			proposal: "- UPDATE mem-00\n  舊：使用者要繁體中文\n  因：x\n",
			wantNote: "提案殘缺",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			seedRecords(t, root, [2]string{"使用者要繁體中文", c.tags})
			writeProposal(t, root, "## [整併] ts\n"+c.proposal)

			applied, skipped, err := ApplyProposedMemory(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(applied) != 0 {
				t.Fatalf("不該套用任何東西: %v", applied)
			}
			if len(skipped) != 1 || !strings.Contains(skipped[0], c.wantNote) {
				t.Fatalf("原因應含 %q，got %v", c.wantNote, skipped)
			}
			// 記錄必須原封不動
			if got := readRecord(t, root, "mem-00"); got != "使用者要繁體中文" {
				t.Errorf("記錄被動到了: %q", got)
			}
			// 被擋下的要【留在提案檔】等重新整併，不能悄悄消失
			if rest := ListProposedMemory(root); len(rest) != 1 {
				t.Errorf("被擋下的應留在提案檔，got %d 條", len(rest))
			}
		})
	}
}

// 混合放行：ADD 成功、破壞性被擋，兩者互不影響，且剩餘條目依原編號排回去。
func TestApply_MixedKeepsOrder(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, [2]string{"目前內容", "慣例"})
	writeProposal(t, root, `## [整併] ts
- 一條普通新事實
- UPDATE mem-00 — 新值
  舊：對不上的舊值
  因：x
- 另一條普通新事實
`)
	applied, skipped, err := ApplyProposedMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || len(skipped) != 1 {
		t.Fatalf("applied=%v skipped=%v", applied, skipped)
	}
	rest := ListProposedMemory(root)
	if len(rest) != 1 || rest[0].Op != OpUpdate {
		t.Fatalf("只該剩那條被擋的 UPDATE: %+v", rest)
	}
	// round-trip：留下來的仍是 UPDATE，附帶行沒掉——否則下次放行會變成憑空新增
	if rest[0].Old == "" || rest[0].Why == "" {
		t.Errorf("附帶行遺失: %+v", rest[0])
	}
}

// 增量：連跑兩次、中間沒有新記錄，第二次應是 no-op（不再花一次 LLM 呼叫）。
func TestReconcile_IncrementalNoOp(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, [2]string{"事實 A", "慣例"}, [2]string{"事實 B 推翻了 A", "慣例"})

	resp := `{"actions":[{"op":"update","n":1,"fact":"事實 A 已被 B 取代","why":"矛盾"}]}`
	first, err := NewMemorySynthesizer(&countingProvider{content: resp}, root).Reconcile(t.Context())
	if err != nil || len(first) != 1 {
		t.Fatalf("第一次應產出提案: %v %v", first, err)
	}

	cp := &countingProvider{content: resp}
	second, err := NewMemorySynthesizer(cp, root).Reconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Errorf("第二次應是 no-op，卻產出 %v", second)
	}
	if cp.calls != 0 {
		t.Errorf("第二次不該呼叫 LLM，calls=%d", cp.calls)
	}

	// 有新記錄進來就要重新整併
	seedNewRecord(t, root, "mem-99", "又一條新事實")
	cp2 := &countingProvider{content: `{"actions":[]}`}
	if _, err := NewMemorySynthesizer(cp2, root).Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if cp2.calls != 1 {
		t.Errorf("有新記錄應重跑，calls=%d", cp2.calls)
	}
}

type countingProvider struct {
	content string
	calls   int
}

func (c *countingProvider) Generate(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	c.calls++
	return &schema.Message{Role: schema.RoleAssistant, Content: c.content}, nil
}
func (c *countingProvider) MaxContextTokens() int { return 200000 }
func (c *countingProvider) ModelName() string     { return "counting" }

func seedNewRecord(t *testing.T, root, slug, desc string) {
	t.Helper()
	p := filepath.Join(root, ".claw", "memory", slug+".md")
	body := fmt.Sprintf("---\nname: n\ndescription: %s\ntags: [慣例]\n---\n%s\n", desc, desc)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// mtime 要明確晚於剛才的整併標記，否則測試會依賴時鐘解析度
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
}

// 窗口只有 N，而呼叫端給的是【依 Path（內容雜湊）排序】的清單——直接切前 N 筆等於隨機抽樣。
// 實測踩過：135 條裡「不開會不上板」三條只有一條落進窗口，對上十一條「要開會上板」，模型
// 判成沒有矛盾。它看到的資料裡確實沒有；而 MarkReconciled 一蓋章，另外 75 條再也不會被看到。
func TestPickForReconcile_ProfileFirstThenRecent(t *testing.T) {
	at := func(d int) time.Time { return time.Date(2026, 8, d, 0, 0, 0, 0, time.UTC) }
	recs := []ctxpkg.MemoryRecord{
		{Path: "a.md", Description: "舊慣例", Recorded: at(1)},
		{Path: "b.md", Description: "畫像·舊", Tags: []string{"user"}, Recorded: at(2)},
		{Path: "c.md", Description: "新慣例", Recorded: at(9)},
		{Path: "d.md", Description: "畫像·新", Tags: []string{"user"}, Recorded: at(8)},
	}
	picked, dropped := pickForReconcile(recs, 3)
	got := []string{picked[0].Description, picked[1].Description, picked[2].Description}
	want := []string{"畫像·新", "畫像·舊", "新慣例"} // 畫像優先（各自新到舊），額度剩下的給最近的慣例
	if !slices.Equal(got, want) {
		t.Errorf("挑錯了\n got %v\nwant %v", got, want)
	}
	if dropped != 1 {
		t.Errorf("該回報漏掉 1 條，got %d", dropped)
	}

	// 沒超量就原封不動——編號與呼叫端的清單一致，不必多一層對映。
	if p, d := pickForReconcile(recs, 10); len(p) != 4 || d != 0 {
		t.Errorf("未超量不該重排或丟棄，got %d 條 / 漏 %d", len(p), d)
	}
}
