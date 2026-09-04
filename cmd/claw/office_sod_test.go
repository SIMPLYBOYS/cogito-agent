package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 職務分離（SoD）：派工鑰匙開不了審批的門，審批鑰匙開不了派工的門。
// 這是 README 記錄的那個洞——「持 token 者可自我放行」——的修法。

type sodCall struct{ channel, user, text string }

func postTask(h http.HandlerFunc, bearer, approverTok, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/task", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	if approverTok != "" {
		req.Header.Set("X-Approver-Token", approverTok)
	}
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func TestSoD_DispatcherCannotBecomeApprover(t *testing.T) {
	var got []sodCall
	h := officeTaskHandlerSoD("dispatch-key", "office-web", "office-boss", "approve-key",
		func(c, u, tx string) { got = append(got, sodCall{c, u, tx}) }, nil)

	// 只拿派工 token 送 approve：進得去，但身分是派工者——Core 那端的 isAdmin 會拒。
	rr := postTask(h, "dispatch-key", "", `{"agent":"p19","text":"approve"}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("派工 token 送 approve 該受理（由 Core 拒），實得 %d", rr.Code)
	}
	if len(got) != 1 || got[0].user != "office-web" {
		t.Fatalf("沒帶審批鑰匙就不該變成審批身分: %+v", got)
	}
}

func TestSoD_ApproverTokenSwitchesIdentity(t *testing.T) {
	var got []sodCall
	h := officeTaskHandlerSoD("dispatch-key", "office-web", "office-boss", "approve-key",
		func(c, u, tx string) { got = append(got, sodCall{c, u, tx}) }, nil)

	rr := postTask(h, "dispatch-key", "approve-key", `{"agent":"p19","text":"approve"}`)
	if rr.Code != http.StatusAccepted || len(got) != 1 || got[0].user != "office-boss" {
		t.Fatalf("帶對審批鑰匙要以 approver 身分送進去: %d %+v", rr.Code, got)
	}
}

func TestSoD_WrongApproverTokenIs401(t *testing.T) {
	called := false
	h := officeTaskHandlerSoD("dispatch-key", "office-web", "office-boss", "approve-key",
		func(string, string, string) { called = true }, nil)
	rr := postTask(h, "dispatch-key", "nope", `{"agent":"p19","text":"approve"}`)
	if rr.Code != http.StatusUnauthorized || called {
		t.Fatalf("錯的審批鑰匙要 401 且不派送: %d called=%v", rr.Code, called)
	}
}

func TestSoD_ApproverCannotDispatchWork(t *testing.T) {
	called := false
	h := officeTaskHandlerSoD("dispatch-key", "office-web", "office-boss", "approve-key",
		func(string, string, string) { called = true }, nil)
	rr := postTask(h, "dispatch-key", "approve-key", `{"agent":"p19","text":"rm -rf / 順便"}`)
	if rr.Code != http.StatusForbidden || called {
		t.Fatalf("審批鑰匙拿來派工要 403：鑰匙分兩把、各開一扇門。實得 %d called=%v", rr.Code, called)
	}
}

func TestSoD_NoApproverConfiguredMeansNoApprovalPower(t *testing.T) {
	var got []sodCall
	// approverToken 為空＝入口沒有審批權：帶任何 X-Approver-Token 都是 401
	h := officeTaskHandlerSoD("dispatch-key", "office-web", "office-boss", "",
		func(c, u, tx string) { got = append(got, sodCall{c, u, tx}) }, nil)
	if rr := postTask(h, "dispatch-key", "anything", `{"agent":"p19","text":"approve"}`); rr.Code != http.StatusUnauthorized {
		t.Fatalf("沒設審批鑰匙時，帶鑰匙的請求要 401，實得 %d", rr.Code)
	}
	// 舊簽名的包裝一樣走空 approver
	h2 := officeTaskHandler("dispatch-key", "office-web", func(c, u, tx string) { got = append(got, sodCall{c, u, tx}) }, nil)
	if rr := postTask(h2, "dispatch-key", "", `{"agent":"p19","text":"hi"}`); rr.Code != http.StatusAccepted {
		t.Fatalf("舊簽名要維持原行為: %d", rr.Code)
	}
}
