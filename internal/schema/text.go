package schema

import "unicode/utf8"

// TruncRunes 按【字元】截斷字串，超長時接上 suffix；未超長則原樣返回。
//
// 【為什麼不能用 s[:n]】Go 的字串切片是按 byte 切的。本專案的工具輸出、報錯訊息、技能描述幾乎
// 都含中文（UTF-8 每字 3 bytes），在第 n 個 byte 切下去有很高機率切在多位元組字元中間，產生
// 非法 UTF-8：聊天端顯示成 �、寫進 YAML frontmatter 會汙染技能檔、送去 OTLP 會讓整批匯出失敗
// （observability/trace.go 就是為了兜這個才在出口做 ToValidUTF8）。
//
// 這個 helper 放在 schema（葉子包，人人可 import）是刻意的：這個 bug 曾同時存在於 5 個地方，
// 正因為每個包都各自手寫一份截斷邏輯——有 3 份寫對、5 份寫錯。收斂成一份，就只有一個地方要對。
func TruncRunes(s string, max int, suffix string) string {
	if max < 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + suffix
}
