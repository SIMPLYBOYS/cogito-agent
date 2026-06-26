package observability

import (
	"testing"
	"unicode/utf8"
)

// 工具輸出含中文時，上游 s[:max] 按位元組截斷會切出非法 UTF-8；toAttr 必須收斂成合法 UTF-8，
// 否則 OTLP 整批匯出失敗（Twinkle Hub 回中文資料時實際踩到）。
func TestToAttr_SanitizesInvalidUTF8(t *testing.T) {
	bad := string([]byte("台北市")[:4]) // 切到多位元組字元中間
	if utf8.ValidString(bad) {
		t.Fatal("測試前提錯誤：bad 應為非法 UTF-8")
	}
	if got := toAttr("preview", bad).Value.AsString(); !utf8.ValidString(got) {
		t.Errorf("屬性值應收斂成合法 UTF-8，got %q", got)
	}
}
