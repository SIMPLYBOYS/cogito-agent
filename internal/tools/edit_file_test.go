package tools

import (
	"strings"
	"testing"
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
