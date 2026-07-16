package engine

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 工具報錯幾乎全是中文；byte 切會切在多位元組字元中間，聊天端顯示成 �。
func TestCapRunes_UTF8Safe(t *testing.T) {
	zh := strings.Repeat("命令執行失敗：找不到該檔案。", 40) // 遠超 200 字元

	got := capRunes(zh, 200)
	if !utf8.ValidString(got) {
		t.Error("截斷後產生了非法 UTF-8（切到多位元組字元中間）")
	}
	if strings.Contains(got, "�") {
		t.Error("截斷後出現替換字元 �")
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "... (已截斷)")); n != 200 {
		t.Errorf("應保留 200 個【字元】，got %d", n)
	}

	// 對照：舊的 byte 切法就是壞的（這行證明這個測試不是白寫的）
	if bad := zh[:200]; utf8.ValidString(bad) {
		t.Skip("此輸入在 200 byte 處剛好對齊字元邊界，換一組再測")
	}

	// 短字串原樣返回、ASCII 也正常
	if got := capRunes("hi", 200); got != "hi" {
		t.Errorf("短字串應原樣返回，got %q", got)
	}
	// 注意 len() 對中文回的是 byte 數——正是本題的陷阱，這裡要數 rune。
	suffixRunes := utf8.RuneCountInString("... (已截斷)")
	if got := capRunes(strings.Repeat("a", 300), 200); utf8.RuneCountInString(got) != 200+suffixRunes {
		t.Errorf("ASCII 截斷長度不對: %d", utf8.RuneCountInString(got))
	}
}
