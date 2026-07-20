package schema

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncRunes(t *testing.T) {
	const zh = "命令執行失敗：找不到該檔案。"

	// 核心：中文截斷後仍是合法 UTF-8（byte 切會在這裡爆）
	long := strings.Repeat(zh, 40)
	got := TruncRunes(long, 200, "…")
	if !utf8.ValidString(got) {
		t.Error("截斷後產生非法 UTF-8")
	}
	if strings.Contains(got, "�") {
		t.Error("截斷後出現替換字元 �")
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "…")); n != 200 {
		t.Errorf("應保留 200 個字元，got %d", n)
	}

	// 對照組：證明 byte 切【真的】會壞——這條紅了才代表上面那些斷言有意義
	if utf8.ValidString(long[:200]) {
		t.Fatal("測試資料選得不好：byte 切在此處剛好對齊，換一組")
	}

	// max 以【字元】計，不是 byte
	if got := TruncRunes(zh, 4, ""); got != "命令執行" {
		t.Errorf("max 應以字元計，got %q", got)
	}
	// 不足長度原樣回傳（不加 suffix）
	if got := TruncRunes(zh, 100, "…"); got != zh {
		t.Errorf("未超長應原樣回傳，got %q", got)
	}
	// 邊界
	if got := TruncRunes("", 10, "…"); got != "" {
		t.Errorf("空字串應原樣，got %q", got)
	}
	if got := TruncRunes("abc", 0, "…"); got != "…" {
		t.Errorf("max=0 應只剩 suffix，got %q", got)
	}
	if got := TruncRunes("abc", -1, "…"); got != "abc" {
		t.Errorf("負 max 應原樣回傳（別 panic），got %q", got)
	}
	// 剛好等於上限不截斷
	if got := TruncRunes("abcd", 4, "…"); got != "abcd" {
		t.Errorf("長度等於 max 不該截斷，got %q", got)
	}
}
