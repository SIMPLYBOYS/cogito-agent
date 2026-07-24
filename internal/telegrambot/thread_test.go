package telegrambot

import (
	"strings"
	"testing"
)

// 3b：論壇主題路由。chatKey 只在 is_topic_message 時附 thread（避免把一般群組的回覆串拆家）；
// parseChatThread 還原 chat_id（可負）+ thread；並驗一趟收→namespace→送的往返一致。
func TestChatKey(t *testing.T) {
	cases := []struct {
		name string
		msg  tgMessage
		want string
	}{
		{"私聊無主題", tgMessage{Chat: tgChat{ID: 123, Type: "private"}}, "123"},
		{"群組無主題", tgMessage{Chat: tgChat{ID: -100, Type: "supergroup"}}, "-100"},
		{"論壇主題", tgMessage{Chat: tgChat{ID: -1001234567890, Type: "supergroup"}, MessageThreadID: 456, IsTopicMessage: true}, "-1001234567890:456"},
		{"General 主題(非 topic message)", tgMessage{Chat: tgChat{ID: -100, Type: "supergroup"}, MessageThreadID: 1, IsTopicMessage: false}, "-100"},
		{"一般回覆串(有 thread 但非主題)不分家", tgMessage{Chat: tgChat{ID: -100, Type: "supergroup"}, MessageThreadID: 789, IsTopicMessage: false}, "-100"},
	}
	for _, c := range cases {
		if got := chatKey(&c.msg); got != c.want {
			t.Errorf("%s: chatKey=%q, 期望 %q", c.name, got, c.want)
		}
	}
}

func TestParseChatThread(t *testing.T) {
	cases := []struct {
		raw     string
		chat    int64
		thread  int
		wantErr bool
	}{
		{"123", 123, 0, false},
		{"123:456", 123, 456, false},
		{"-1001234567890:456", -1001234567890, 456, false},
		{"-100", -100, 0, false},
		{"abc", 0, 0, true},
		{"123:xx", 123, 0, false}, // thread 解不出 → 容錯當無 thread
	}
	for _, c := range cases {
		chat, thread, err := parseChatThread(c.raw)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v 期望 wantErr=%v", c.raw, err, c.wantErr)
			continue
		}
		if err == nil && (chat != c.chat || thread != c.thread) {
			t.Errorf("%q: (chat=%d thread=%d), 期望 (%d,%d)", c.raw, chat, thread, c.chat, c.thread)
		}
	}
}

// 往返：收訊 chatKey → core namespace（platform:key，只切第一個冒號）→ 送訊 parseChatThread。
// 負的超級群組 id 內含 '-' 不含 ':'，故 namespace 的 Cut 與 thread 的 Cut 不會誤切。
func TestThreadRoundTrip(t *testing.T) {
	m := tgMessage{Chat: tgChat{ID: -1001234567890, Type: "supergroup"}, MessageThreadID: 42, IsTopicMessage: true}
	key := chatKey(&m) // "-1001234567890:42"

	convID := "telegram:" + key           // core.convID 的組法
	_, raw, _ := strings.Cut(convID, ":") // SendMessage 的 Cut（只切第一個冒號）
	if raw != key {
		t.Fatalf("namespace 往返破損：raw=%q 期望 %q", raw, key)
	}
	chat, thread, err := parseChatThread(raw)
	if err != nil || chat != -1001234567890 || thread != 42 {
		t.Errorf("送訊還原錯誤：chat=%d thread=%d err=%v", chat, thread, err)
	}
}
