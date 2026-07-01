package context

import (
	"os"
	"path/filepath"
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
