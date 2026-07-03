package telegrambot

import (
	"regexp"
	"strings"
	"testing"
)

// 群組 @提及剝離：DM 全收；群組只在 @我 / 回覆我 時當任務，並剝掉 @提及。
func TestAddressedText(t *testing.T) {
	b := &TelegramBot{botID: 42, mention: regexp.MustCompile(`(?i)@cogito_bot`)}
	dm := tgChat{ID: 1, Type: "private"}
	grp := tgChat{ID: -100, Type: "supergroup"}
	me := &tgUser{ID: 42}
	other := &tgUser{ID: 7}

	cases := []struct {
		name     string
		msg      tgMessage
		wantText string
		wantOK   bool
	}{
		{"DM 全收", tgMessage{Text: "做這個", Chat: dm}, "做這個", true},
		{"DM 空", tgMessage{Text: "  ", Chat: dm}, "", false},
		{"群組 @我 剝提及", tgMessage{Text: "@cogito_bot 幫我建檔", Chat: grp}, "幫我建檔", true},
		{"群組 大小寫不敏感", tgMessage{Text: "@Cogito_Bot hi", Chat: grp}, "hi", true},
		{"群組 沒叫我 → 忽略", tgMessage{Text: "大家好啊", Chat: grp}, "", false},
		{"群組 只 @我沒內容 → 忽略", tgMessage{Text: "@cogito_bot", Chat: grp}, "", false},
		{"群組 回覆到我", tgMessage{Text: "繼續", Chat: grp, ReplyToMessage: &tgMessage{From: me}}, "繼續", true},
		{"群組 回覆到別人 → 忽略", tgMessage{Text: "繼續", Chat: grp, ReplyToMessage: &tgMessage{From: other}}, "", false},
	}
	for _, c := range cases {
		got, ok := b.addressedText(&c.msg)
		if got != c.wantText || ok != c.wantOK {
			t.Errorf("%s: addressedText = (%q,%v), want (%q,%v)", c.name, got, ok, c.wantText, c.wantOK)
		}
	}
}

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

// 畸形 JSON（截斷 body 等）應回 error，讓 Start 退避重試而非崩潰或當空。
func TestParseUpdates_MalformedJSON(t *testing.T) {
	if _, err := parseUpdates(strings.NewReader(`{"ok":true,"result":[{"update`)); err == nil {
		t.Error("截斷的 JSON 應回 error")
	}
}

// ok:false（token 失效/限流等 API 錯誤）應回 error 並帶 description，不再被靜默吞掉當作沒新訊息。
func TestParseUpdates_NotOK(t *testing.T) {
	_, err := parseUpdates(strings.NewReader(`{"ok":false,"description":"Unauthorized"}`))
	if err == nil {
		t.Fatal("ok:false 應回 error")
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("error 應帶 API description，got %v", err)
	}
}
