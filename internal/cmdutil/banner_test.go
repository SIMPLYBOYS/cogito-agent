package cmdutil

import (
	"strings"
	"testing"
)

func TestBannerLines(t *testing.T) {
	lines := bannerLines()
	if len(lines) != 7 {
		t.Fatalf("banner 應為 7 列，got %d", len(lines))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "█") {
		t.Error("banner 應由 █ 方塊字元組成")
	}
}

func TestRenderBanner(t *testing.T) {
	plain := renderBanner(false)
	if !strings.Contains(plain, "cogito, ergo ago") {
		t.Error("應含標語")
	}
	if strings.Contains(plain, "\x1b[") {
		t.Error("無色版不該有 ANSI 轉義")
	}
	if !strings.Contains(renderBanner(true), "\x1b[38;2;") {
		t.Error("彩色版應有 24-bit ANSI 轉義")
	}
}
