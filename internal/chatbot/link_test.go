package chatbot

import (
	"strings"
	"testing"
)

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

// 連結身分的 DM 在【跨 Core】共用同一個 workDir，忙碌鎖必須跟著跨 Core 生效——否則同一份 session
// 被兩個平台併發 Run，history 交錯出壞歷史並落盤。這條釘死 running 是 package 級的。
func TestSharedIdentity_LockIsCrossCore(t *testing.T) {
	t.Setenv("COGITO_USER_LINK", "771163423=U0AABBCC")
	root := t.TempDir()
	tg := NewCore("tgx1", root, nil, func(string, string) {})
	sl := NewCore("slx1", root, nil, func(string, string) {})

	tgWD := tg.channelWorkDir(tg.stateID("771163423", tg.convID("771163423"), true))
	slWD := sl.channelWorkDir(sl.stateID("U0AABBCC", sl.convID("D123"), true))
	if tgWD != slWD {
		t.Fatalf("連結身分的 DM 應共用 workDir：\n tg=%s\n sl=%s", tgWD, slWD)
	}
	t.Cleanup(func() { tg.release(tgWD) })

	if !tg.tryAcquire(tgWD, func() {}) {
		t.Fatal("Telegram 應取得鎖")
	}
	if sl.tryAcquire(slWD, func() {}) {
		t.Fatal("Slack 不該在另一個 Core 已持有同一 workDir 時取得鎖——併發 Run 會磚化該 session")
	}
	if !sl.stop(slWD) {
		t.Error("/stop 應能跨平台中止同一份共享 session 的任務")
	}
}

// 授權閘必須先於任何副作用：被拒者不得改寫共享身分的回覆路由，且拒絕訊息要回到它自己的頻道。
func TestUnauthorizedCannotHijackRoute(t *testing.T) {
	t.Setenv("COGITO_USER_LINK", "771163423=U0AABBCC")
	victim, _ := newCaptureCore(t, "tgvic", []string{"771163423"}, nil)
	// 攻擊者：ID 在 USER_LINK 但【不在】allowlist（從 allowlist 撤掉卻忘了清 USER_LINK）
	attacker, attackerMsgs := newCaptureCore(t, "slatk", []string{"771163423"}, nil)

	// 受害者先建立路由。用 help（會被指令閘消費）而非一般任務——後者會真的起 handleAgentRun，
	// 而測試 Core 的 factory 是 nil。路由在授權閘之後、指令閘之前記錄，所以照樣會寫進去。
	victim.dispatch("771163423", "771163423", "help", true)
	before, _ := lastRoute.Load("user:771163423")
	if before == nil {
		t.Fatal("受害者的路由應已建立")
	}

	attacker.dispatch("D999", "U0AABBCC", "hi", true) // 被拒者試圖翻轉路由

	after, _ := lastRoute.Load("user:771163423")
	if after != before {
		t.Errorf("未授權者改寫了共享身分的回覆路由：%v → %v（審批請求會被送到他那裡）", before, after)
	}
	if got := lastOf(attackerMsgs); !strings.Contains(got, "未授權") {
		t.Errorf("拒絕訊息應直送對方頻道（他需要知道自己的 user ID），got %q", got)
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

// 平台限定名單：user id 名單原本只存裸 id，一個 id 在【每個】平台都生效。今天安全純屬 ID 空間
// 不重疊的巧合（Telegram 純數字 / Slack U 開頭）；加入第三個平台（Discord snowflake 也是純數字）
// 那天，一個 Discord 使用者的 ID 只要等於某個白名單裡的 Telegram ID 就直接獲得授權。
func TestScopedUserSets(t *testing.T) {
	// 模擬「Telegram 的 771163423 被授權」，而 Discord 有個同號使用者
	t.Setenv("COGITO_ALLOWED_USERS", "telegram:771163423, U0BARE")
	t.Setenv("COGITO_ADMIN_USERS", "telegram:771163423")
	tg := NewCore("telegram", t.TempDir(), nil, func(string, string) {})
	dc := NewCore("discord", t.TempDir(), nil, func(string, string) {})

	// 限定平台：只有 telegram 那個 771163423 過得了
	if !tg.isAllowed("771163423") {
		t.Error("telegram:771163423 應被授權")
	}
	if dc.isAllowed("771163423") {
		t.Error("Discord 上的同號使用者不該因 ID 碰撞獲得授權——這正是平台限定要防的")
	}
	if !tg.isAdmin("771163423") {
		t.Error("telegram:771163423 應是管理員")
	}
	if dc.isAdmin("771163423") {
		t.Error("Discord 同號者不該獲得審批權")
	}

	// 裸 id：向後相容——任何平台皆生效（既有 .env 不必改）
	if !tg.isAllowed("U0BARE") || !dc.isAllowed("U0BARE") {
		t.Error("裸 id 應在任何平台生效（向後相容）")
	}
	// 不在名單者一律拒絕
	if tg.isAllowed("stranger") || dc.isAllowed("stranger") {
		t.Error("不在名單者不該被授權")
	}
}

// userLink 同樣支援平台限定：Discord 同號者不得繼承他人的 DM session。
func TestScopedUserLink(t *testing.T) {
	t.Setenv("COGITO_USER_LINK", "telegram:771163423=slack:U0AABBCC")
	tg := NewCore("telegram", t.TempDir(), nil, func(string, string) {})
	sl := NewCore("slack", t.TempDir(), nil, func(string, string) {})
	dc := NewCore("discord", t.TempDir(), nil, func(string, string) {})

	want := "user:telegram:771163423"
	if got := tg.stateID("771163423", tg.convID("771163423"), true); got != want {
		t.Errorf("telegram 應歸一到 %q，got %q", want, got)
	}
	if got := sl.stateID("U0AABBCC", sl.convID("D1"), true); got != want {
		t.Errorf("slack 應歸一到同一把 key %q，got %q", want, got)
	}
	// Discord 同號者：不在 link 裡，維持自己的平台 session，不得繼承
	if got := dc.stateID("771163423", dc.convID("771163423"), true); got != "discord:771163423" {
		t.Errorf("Discord 同號者不該繼承他人的共享 session，got %q", got)
	}
}
