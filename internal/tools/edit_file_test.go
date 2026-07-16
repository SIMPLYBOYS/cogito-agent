package tools

import (
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

func TestReindent(t *testing.T) {
	cases := []struct {
		name    string
		newText string
		base    string
		want    string
	}{
		{
			name:    "模型給淺縮進，補齊到深層 base，內部相對結構保留",
			newText: "newA()\nif y {\n    newB()\n}", // flush-left，內部巢狀 4 空格
			base:    strings.Repeat(" ", 12),
			want: strings.Repeat(" ", 12) + "newA()\n" +
				strings.Repeat(" ", 12) + "if y {\n" +
				strings.Repeat(" ", 16) + "newB()\n" +
				strings.Repeat(" ", 12) + "}",
		},
		{
			name:    "模型自帶 4 空格縮進，dedent 後再 re-base，不 double-indent",
			newText: "    a()\n        b()\n    c()", // 4/8/4 → 最小共同 4
			base:    strings.Repeat(" ", 8),
			want: strings.Repeat(" ", 8) + "a()\n" +
				strings.Repeat(" ", 12) + "b()\n" +
				strings.Repeat(" ", 8) + "c()",
		},
		{
			name:    "空白行歸零，不留尾隨空白",
			newText: "a()\n\nb()",
			base:    strings.Repeat(" ", 4),
			want:    strings.Repeat(" ", 4) + "a()\n\n" + strings.Repeat(" ", 4) + "b()",
		},
		{
			name:    "單行",
			newText: "doSomething()",
			base:    "\t\t",
			want:    "\t\tdoSomething()",
		},
	}
	for _, c := range cases {
		if got := reindent(c.newText, c.base); got != c.want {
			t.Errorf("%s\n got=%q\nwant=%q", c.name, got, c.want)
		}
	}
}

// L4 逐行匹配命中後，替換內容應自動對齊到匹配塊的基礎縮進（這是本次改進的核心）。
func TestLineByLineReplace_ReindentsToMatchedBlock(t *testing.T) {
	content := strings.Join([]string{
		"func outer() {",
		strings.Repeat(" ", 8) + "for i := range xs {",
		strings.Repeat(" ", 16) + "oldA()",
		strings.Repeat(" ", 16) + "oldB()",
		strings.Repeat(" ", 8) + "}",
		"}",
	}, "\n")

	// 模型給的 old/new 都是 flush-left（縮進與目標塊的 16 空格不符）
	oldText := "oldA()\noldB()"
	newText := "newA()\nif y {\n    newB()\n}"

	got, err := lineByLineReplace(content, oldText, newText)
	if err != nil {
		t.Fatalf("不應報錯: %v", err)
	}

	want := strings.Join([]string{
		"func outer() {",
		strings.Repeat(" ", 8) + "for i := range xs {",
		strings.Repeat(" ", 16) + "newA()",
		strings.Repeat(" ", 16) + "if y {",
		strings.Repeat(" ", 20) + "newB()", // 16(base) + 4(相對) = 20
		strings.Repeat(" ", 16) + "}",
		strings.Repeat(" ", 8) + "}",
		"}",
	}, "\n")

	if got != want {
		t.Errorf("替換後縮進未正確對齊\n got=\n%q\nwant=\n%q", got, want)
	}
}

// 若模型給的 newText 已是正確縮進，re-base 應為等效（冪等），不破壞已對齊的內容。
func TestLineByLineReplace_AlreadyCorrectIndentIsStable(t *testing.T) {
	content := strings.Join([]string{
		"func f() {",
		strings.Repeat(" ", 8) + "old()",
		"}",
	}, "\n")
	oldText := "old()"
	newText := strings.Repeat(" ", 8) + "newOne()\n" + strings.Repeat(" ", 8) + "newTwo()"

	got, err := lineByLineReplace(content, oldText, newText)
	if err != nil {
		t.Fatalf("不應報錯: %v", err)
	}
	want := strings.Join([]string{
		"func f() {",
		strings.Repeat(" ", 8) + "newOne()",
		strings.Repeat(" ", 8) + "newTwo()",
		"}",
	}, "\n")
	if got != want {
		t.Errorf("已正確縮進的 newText 應穩定對齊\n got=%q\nwant=%q", got, want)
	}
}

// edit_file 的錯誤字串與 context.RecoveryManager 的 pattern 是【跨套件字串耦合】（不能用 typed
// sentinel error：tools 已 import context，反向會成環）。這條測試走【真實】的錯誤字串跑完整條鏈——
// recovery_test.go 那邊用的是手寫字串，耦合斷了也不會紅（"匹配到了多處" 這個 pattern 就這樣死在
// 那裡沒人發現，而 L1 多重匹配的錯誤一直拿不到救援指南）。改任一邊的用詞，這裡會紅。
func TestEditFileErrors_AllGetRecoveryHint(t *testing.T) {
	rm := ctxpkg.NewRecoveryManager()
	const content = "line1\nline2\nline3\n"

	cases := []struct {
		name    string
		old     string // 觸發該錯誤路徑的 old_text
		wantKey string // 救援指南裡該出現的關鍵字
	}{
		{"L1 多重匹配", "line1\nline2\nline3\nline1\nline2\nline3", "唯一性"}, // 見下方另建的 content
		{"完全找不到", "totally-absent-text", "read_file"},
		{"old_text 比檔案長", strings.Repeat("x\n", 20), "read_file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := content
			if tc.name == "L1 多重匹配" {
				src = "dup\ndup\n" // 讓 old_text="dup" 精確命中兩處
				tc.old = "dup"
			}
			_, err := fuzzyReplace(src, tc.old, "new")
			if err == nil {
				t.Fatalf("預期失敗，卻成功")
			}
			seen := rm.AnalyzeAndInject("edit_file", err.Error())
			t.Logf("模型實際讀到：%s", seen)
			if !strings.Contains(seen, "[系統救援指南]") {
				t.Errorf("這條錯誤路徑拿不到救援指南（recovery.go 的 pattern 對不上）：%v", err)
			}
			if !strings.Contains(seen, tc.wantKey) {
				t.Errorf("救援指南應含 %q，got: %s", tc.wantKey, seen)
			}
		})
	}
}

// 走到「找不到」代表 fuzzyReplace 的 L3/L4 已經用 TrimSpace 吸收過縮排差異了，
// 此時再叫模型「檢查縮進」是把它推回盲目重試的迴圈。
func TestRecoveryHint_NoIndentMisdirection(t *testing.T) {
	_, err := fuzzyReplace("line1\nline2\n", "totally-absent", "x")
	if err == nil {
		t.Fatal("預期失敗")
	}
	if seen := ctxpkg.NewRecoveryManager().AnalyzeAndInject("edit_file", err.Error()); strings.Contains(seen, "縮進") {
		t.Errorf("救援指南不該指向縮排（L4 已逐行 TrimSpace 比對過）：%s", seen)
	}
}
