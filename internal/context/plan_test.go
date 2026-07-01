package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTodo(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "TODO.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 帳本解讀是斷點續跑的權威來源：算錯「下一步」= 重做或漏做，故釘死解析。
func TestReadTodoProgress_PicksFirstUnchecked(t *testing.T) {
	dir := writeTodo(t, `# 計畫
- [x] 建目錄
- [X] 寫 schema
- [ ] 實作 handler
- [ ] 寫測試
`)
	p, ok := ReadTodoProgress(dir)
	if !ok {
		t.Fatal("應偵測到帳本")
	}
	if p.Total != 4 || p.Done != 2 {
		t.Errorf("Total/Done = %d/%d, want 4/2", p.Total, p.Done)
	}
	if p.NextStep != "實作 handler" {
		t.Errorf("下一步應為第一個未打勾項，got %q", p.NextStep)
	}
}

func TestReadTodoProgress_AllDone(t *testing.T) {
	dir := writeTodo(t, "- [x] a\n- [x] b\n")
	p, ok := ReadTodoProgress(dir)
	if !ok || p.Total != 2 || p.Done != 2 || p.NextStep != "" {
		t.Errorf("全完成應 NextStep 空：%+v ok=%v", p, ok)
	}
}

func TestReadTodoProgress_NoLedger(t *testing.T) {
	if _, ok := ReadTodoProgress(t.TempDir()); ok { // 無 TODO.md
		t.Error("無 TODO.md 應回 false")
	}
	dir := writeTodo(t, "# 只有散文，沒有 checkbox\n一些說明。\n")
	if _, ok := ReadTodoProgress(dir); ok {
		t.Error("無 checkbox 應回 false（不注入進度錨）")
	}
}

func TestReadPlanGoal(t *testing.T) {
	dir := t.TempDir()
	// 去標題行、留實質目標。
	if err := os.WriteFile(filepath.Join(dir, "PLAN.md"), []byte("# 專案計畫\n目標：把使用者上傳的 CSV 轉成月報表並寄出。\n技術選型：Go + gomail。\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	goal, ok := ReadPlanGoal(dir)
	if !ok {
		t.Fatal("應讀到目標")
	}
	if strings.HasPrefix(goal, "#") {
		t.Errorf("應去掉開頭標題行，got %q", goal)
	}
	if !strings.Contains(goal, "月報表") {
		t.Errorf("應含實質目標，got %q", goal)
	}
}

func TestReadPlanGoal_MissingOrEmpty(t *testing.T) {
	if _, ok := ReadPlanGoal(t.TempDir()); ok {
		t.Error("無 PLAN.md 應回 false")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "PLAN.md"), []byte("# 只有標題\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadPlanGoal(dir); ok {
		t.Error("只有標題無內容應回 false")
	}
}

func TestReadPlanGoal_Clamps(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "PLAN.md"), []byte(strings.Repeat("目標很長", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	goal, ok := ReadPlanGoal(dir)
	if !ok || !strings.Contains(goal, "截斷") {
		t.Errorf("超長應截斷，got ok=%v len=%d", ok, len([]rune(goal)))
	}
}

func TestParseCheckbox(t *testing.T) {
	cases := []struct {
		line     string
		wantOK   bool
		wantDone bool
		wantText string
	}{
		{"- [ ] 做事", true, false, "做事"},
		{"- [x] 完成", true, true, "完成"},
		{"* [X] 星號也行", true, true, "星號也行"},
		{"  - [ ] 未修剪", false, false, ""}, // parseCheckbox 不自行 trim（呼叫端已 TrimSpace）
		{"- [] 壞格式", false, false, ""},
		{"普通文字", false, false, ""},
		{"- 沒有框", false, false, ""},
	}
	for _, c := range cases {
		done, text, ok := parseCheckbox(c.line)
		if ok != c.wantOK || done != c.wantDone || text != c.wantText {
			t.Errorf("parseCheckbox(%q) = (%v,%q,%v), want (%v,%q,%v)",
				c.line, done, text, ok, c.wantDone, c.wantText, c.wantOK)
		}
	}
}
