package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncate_RuneSafe(t *testing.T) {
	long := strings.Repeat("中", maxPreviewChars+50)
	got := truncate(long, maxPreviewChars)
	if !utf8.ValidString(got) {
		t.Fatal("截斷後應為合法 UTF-8（不可切到多位元組字元中間）")
	}
	if want := maxPreviewChars + len("..."); utf8.RuneCountInString(got) != want {
		t.Errorf("rune 數應為 %d，got %d", want, utf8.RuneCountInString(got))
	}
	if short := truncate("短", maxPreviewChars); short != "短" {
		t.Errorf("未超長不應截斷，got %q", short)
	}
}
