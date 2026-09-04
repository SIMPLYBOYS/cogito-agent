package chatbot

import "testing"

// office 平台【不】把 ALLOWED 繼承成 ADMIN——派工者是機器身分（橋），繼承下去就是
// 「持 token 者可自我放行」。IM 平台維持原行為：可對話者即可審批（那些身分是人）。
func TestOfficeDoesNotInheritAdmins(t *testing.T) {
	t.Setenv("COGITO_ALLOWED_USERS", "office-web,U123")
	t.Setenv("COGITO_ADMIN_USERS", "")
	noop := func(string, string) {}

	office := NewCore("office", t.TempDir(), nil, noop)
	if office.isAdmin("office-web") {
		t.Fatal("office 平台的派工者不該因為沒設 ADMIN 就拿到審批權——這正是要封的洞")
	}
	if !office.isAllowed("office-web") {
		t.Fatal("前置條件：派工者仍在 allowed 名單")
	}

	slack := NewCore("slack", t.TempDir(), nil, noop)
	if !slack.isAdmin("U123") {
		t.Fatal("IM 平台維持原行為：沒設 ADMIN 時可對話者即可審批")
	}
}

// 顯式設了 ADMIN，office 的 approver 身分才拿到審批權；派工者仍然沒有。
func TestOfficeExplicitAdminOnly(t *testing.T) {
	t.Setenv("COGITO_ALLOWED_USERS", "office-web,office-boss")
	t.Setenv("COGITO_ADMIN_USERS", "office-boss")
	c := NewCore("office", t.TempDir(), nil, func(string, string) {})
	if !c.isAdmin("office-boss") || c.isAdmin("office-web") {
		t.Fatalf("審批權要落在顯式設定的身分上：boss=%v web=%v", c.isAdmin("office-boss"), c.isAdmin("office-web"))
	}
}
