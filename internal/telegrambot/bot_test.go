package telegrambot

import (
	"strings"
	"testing"
)

// parseUpdates 是入站解析的非平凡處：確認從 getUpdates 回應抽得 chat.id / text / is_bot，
// 並按 update_id 推進 offset。
func TestParseUpdates(t *testing.T) {
	body := `{"ok":true,"result":[
		{"update_id":10,"message":{"text":"hello","chat":{"id":42},"from":{"is_bot":false}}},
		{"update_id":11,"message":{"text":"x","chat":{"id":-100},"from":{"is_bot":true}}}
	]}`
	us, err := parseUpdates(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 2 {
		t.Fatalf("應解析 2 筆 update，got %d", len(us))
	}
	if us[0].Message.Chat.ID != 42 || us[0].Message.Text != "hello" || us[0].Message.From.IsBot {
		t.Errorf("第一筆解析錯誤: %+v", us[0].Message)
	}
	if !us[1].Message.From.IsBot { // 機器人訊息要能被識別以過濾迴環
		t.Error("第二筆應標記 is_bot")
	}
	if us[1].UpdateID != 11 {
		t.Errorf("offset 推進依據 update_id，got %d", us[1].UpdateID)
	}
}
