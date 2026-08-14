package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 評審替身：全過／全不過。真評審是 LLM（styleJudge），判準邏輯本身在這裡用替身驗。
func passAll(ls []string) []bool {
	out := make([]bool, len(ls))
	for i := range out {
		out[i] = true
	}
	return out
}
func failAll(ls []string) []bool { return make([]bool, len(ls)) }

func autopassRoot(t *testing.T, proposals string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claw", ProposedMemoryFileName),
		[]byte(proposals), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAutoApply, "1")
	return root
}

// 四條判準各自都能單獨把一條提案擋下來——擋下的留在提案檔等人審，不是消失。
func TestAutopass_FourGates(t *testing.T) {
	long := strings.Repeat("很長的敘事", 30) // >100 rune：③範圍窄不過
	root := autopassRoot(t, `## [慣例] 來自任務「A」（ts）
- 回覆一律使用繁體中文
- `+long+`
`)
	applied, err := AutoApplyAdditions(root, passAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "回覆一律使用繁體中文" {
		t.Fatalf("只該放行短的那條，got %v", applied)
	}
	rest := readFileIgnore(filepath.Join(root, ".claw", ProposedMemoryFileName))
	if !strings.Contains(rest, "很長的敘事") {
		t.Error("超長那條該留在提案檔等人審")
	}

	// ①風格判定 false → 一條都不過（fail-closed 由 nil judge 那條測）
	root2 := autopassRoot(t, "## [慣例] 來自任務「A」（ts）\n- 部署前先跑 make verify\n")
	if applied, _ := AutoApplyAdditions(root2, failAll); applied != nil {
		t.Fatalf("評審說會改決策行為，不該放行：%v", applied)
	}
	if PendingProposals(root2) != 1 {
		t.Error("被擋的要留在提案檔")
	}

	// judge=nil（評審壞掉）→ fail-closed，全部留審
	if applied, _ := AutoApplyAdditions(root2, nil); applied != nil {
		t.Fatalf("評審不可用時要 fail-closed，got %v", applied)
	}

	// ④衝突零命中：先放一條進記憶庫，再提一條幾乎一樣的 → 撞衝突，留審
	root3 := autopassRoot(t, "## [慣例] 來自任務「A」（ts）\n- 回覆一律使用繁體中文\n")
	if _, err := AutoApplyAdditions(root3, passAll); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root3, ".claw", ProposedMemoryFileName),
		[]byte("## [慣例] 來自任務「B」（ts）\n- 回覆請使用繁體中文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if applied, _ := AutoApplyAdditions(root3, passAll); applied != nil {
		t.Fatalf("撞到既有記憶的該留給人審，got %v", applied)
	}
}

// 放行的掛撤回窗：undo 歸檔那個檔（可復原）、帳上移除；過窗的自動從帳上消失。
func TestAutopass_RevokeWindow(t *testing.T) {
	root := autopassRoot(t, "## [慣例] 來自任務「A」（ts）\n- 回覆一律使用繁體中文\n")
	if _, err := AutoApplyAdditions(root, passAll); err != nil {
		t.Fatal(err)
	}
	live := AutopassPending(root)
	if len(live) != 1 || !strings.Contains(live[0].Desc, "繁體中文") {
		t.Fatalf("撤回窗該有 1 筆，got %+v", live)
	}
	// 帳上的檔名要真的對到記錄檔（內容定址），撤回才撤得到
	if _, err := os.Stat(filepath.Join(root, ".claw", "memory", live[0].File)); err != nil {
		t.Fatalf("帳上檔名對不到記錄檔：%v", err)
	}
	desc, err := RevokeAutopass(root, 1)
	if err != nil || !strings.Contains(desc, "繁體中文") {
		t.Fatalf("撤回失敗：%v / %s", err, desc)
	}
	if files, _ := filepath.Glob(filepath.Join(root, ".claw", "memory", "mem-*.md")); len(files) != 0 {
		t.Error("撤回後記錄檔該離開記憶庫")
	}
	if files, _ := filepath.Glob(filepath.Join(root, ".claw", "memory-archive", "mem-*.md")); len(files) != 1 {
		t.Error("撤回是歸檔不是刪除——要可復原")
	}
	if len(AutopassPending(root)) != 0 {
		t.Error("撤回後帳上要清掉")
	}

	// 過窗：塵埃落定，從帳上消失，undo 再也對不到
	root2 := autopassRoot(t, "## [慣例] 來自任務「A」（ts）\n- 表格一律用代碼塊\n")
	if _, err := AutoApplyAdditions(root2, passAll); err != nil {
		t.Fatal(err)
	}
	stale := loadAutopass(root2)
	stale[0].At = time.Now().Add(-AutopassWindow - time.Hour)
	saveAutopass(root2, stale)
	if len(AutopassPending(root2)) != 0 {
		t.Error("過窗的要自動從帳上消失")
	}
	if _, err := RevokeAutopass(root2, 1); err == nil {
		t.Error("過窗即定案，不能再撤")
	}
}
