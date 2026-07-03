package chatbot

import (
	"strings"
	"testing"
)

func TestArgPreviewAndCapRunes(t *testing.T) {
	// argPreview 去換行（避免多行洗版）
	if got := argPreview("a\nb\r\nc", 100); strings.ContainsAny(got, "\n\r") {
		t.Errorf("應去掉換行，got %q", got)
	}
	// rune-safe 截斷：中文不切壞，且補截斷標記
	out := capRunes(strings.Repeat("測", 300), 10)
	if !strings.HasSuffix(out, "（已截斷）") {
		t.Errorf("超長應補截斷標記，got %q", out)
	}
	if body := strings.TrimSuffix(out, "…（已截斷）"); len([]rune(body)) != 10 {
		t.Errorf("應截到 10 個 rune，got %d", len([]rune(body)))
	}
	// 未超長不動
	if capRunes("短", 10) != "短" {
		t.Error("未超長不應改動")
	}
}
