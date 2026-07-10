package chatbot

import "testing"

func TestParseUserLink(t *testing.T) {
	link := parseUserLink(" 771163423 = U0AABBCC , 999=U0XYZ=D42 ")
	want := map[string]string{
		"771163423": "771163423", "U0AABBCC": "771163423",
		"999": "999", "U0XYZ": "999", "D42": "999",
	}
	if len(link) != len(want) {
		t.Fatalf("期望 %d 條映射，got %d：%v", len(want), len(link), link)
	}
	for id, canon := range want {
		if link[id] != canon {
			t.Errorf("link[%q] = %q，want %q", id, link[id], canon)
		}
	}
	if len(parseUserLink("")) != 0 {
		t.Error("空字串應得空映射")
	}
}

func TestStateID_DMLinked(t *testing.T) {
	t.Setenv("COGITO_USER_LINK", "771163423=U0AABBCC")
	tg := NewCore("tgtest1", t.TempDir(), nil, func(string, string) {})
	sl := NewCore("sltest1", t.TempDir(), nil, func(string, string) {})

	// 同一人在兩平台的 DM → 歸一到同一狀態 key
	a := tg.stateID("771163423", tg.convID("771163423"), true)
	b := sl.stateID("U0AABBCC", sl.convID("D123"), true)
	if a != "user:771163423" || a != b {
		t.Fatalf("連結 DM 應共用狀態 key：tg=%q slack=%q", a, b)
	}
	// 群組不合併（即便發訊者已連結）
	if got := tg.stateID("771163423", tg.convID("-100999"), false); got != "tgtest1:-100999" {
		t.Errorf("群組不應歸一，got %q", got)
	}
	// 未連結使用者的 DM 維持平台 key
	if got := tg.stateID("555", tg.convID("555"), true); got != "tgtest1:555" {
		t.Errorf("未連結 DM 不應歸一，got %q", got)
	}
}

func TestUserRouteForwarding(t *testing.T) {
	got := make(chan [2]string, 1)
	senders.Store("tgx", func(raw, text string) { got <- [2]string{raw, text} })
	defer senders.Delete("tgx")
	lastRoute.Store("user:U1", "tgx:123")
	defer lastRoute.Delete("user:U1")

	SendMessage("user:U1", "hi") // 經 "user" 偽平台轉發到 lastRoute 記錄的實際平台
	select {
	case m := <-got:
		if m[0] != "123" || m[1] != "hi" {
			t.Fatalf("轉發錯誤：%v", m)
		}
	default:
		t.Fatal("user: 出訊未被轉發到最後說話的平台")
	}
	SendMessage("user:unknown", "x") // 查無路由：靜默丟棄、不得 panic
}
