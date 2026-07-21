package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 護欄的判準是 INSECURE 旗標而非綁定位址，訊息必須據此措辭——否則 loopback+旗標的人
// 會看到「非 loopback」而完全找不到原因。這是實際踩過的認知落差。
func TestSecretsDenied_MessageNamesTheFlagNotLoopback(t *testing.T) {
	t.Setenv("COGITO_DASH_INSECURE", "1")
	if secretsAllowed() {
		t.Fatal("前置條件：設了旗標應停用祕密")
	}
	for _, act := range []string{"顯示", "編輯"} {
		msg := secretsDeniedMsg(act)
		if !strings.Contains(msg, "COGITO_DASH_INSECURE") {
			t.Errorf("訊息應點名旗標: %q", msg)
		}
		if strings.Contains(msg, "非 loopback") {
			t.Errorf("訊息不該說「非 loopback」——判準不是綁定位址: %q", msg)
		}
	}
}

// 端點實際回的內容也要是新訊息（四個入口共用 secretsDeniedMsg，別漏接）。
func TestSecretEndpoints_ReturnFlagAwareMessage(t *testing.T) {
	t.Setenv("COGITO_DASH_INSECURE", "1")
	s := &server{}
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		req  *http.Request
	}{
		{"secretReveal", s.secretReveal, httptest.NewRequest("GET", "/secret/reveal?key=ANTHROPIC_API_KEY", nil)},
		{"secretSave", s.secretSave, httptest.NewRequest("POST", "/secret", nil)},
		{"mcpSecretReveal", s.mcpSecretReveal, httptest.NewRequest("GET", "/mcp/secret/reveal", nil)},
		{"mcpSecretSave", s.mcpSecretSave, httptest.NewRequest("POST", "/mcp/secret", nil)},
	} {
		w := httptest.NewRecorder()
		tc.h(w, tc.req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: 應 403，得 %d", tc.name, w.Code)
		}
		if !strings.Contains(w.Body.String(), "COGITO_DASH_INSECURE") {
			t.Errorf("%s: 訊息未點名旗標: %q", tc.name, w.Body.String())
		}
	}
}

// 停用時整區不該憑空消失——要說明原因，否則使用者只會覺得功能壞了。
func TestPlatformTemplate_ExplainsWhySecretsHidden(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		var b strings.Builder
		d := platformData{SecretsAllowed: allowed}
		if err := platformTmpl.Execute(&b, d); err != nil {
			t.Fatalf("SecretsAllowed=%v 模板 render 失敗: %v", allowed, err)
		}
		out := b.String()
		if allowed {
			continue
		}
		if !strings.Contains(out, "COGITO_DASH_INSECURE") {
			t.Error("停用時應說明原因，不可整區靜默消失")
		}
	}
}
