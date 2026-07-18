package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// CSRF 防線矩陣：POST 會執行 agent，必須信 Sec-Fetch-Site、擋跨站。
func TestSameOrigin_CSRF(t *testing.T) {
	cases := []struct {
		name       string
		secFetch   string
		origin     string
		host       string
		wantAllow  bool
	}{
		{"same-origin header", "same-origin", "", "x", true},
		{"none header（使用者自輸網址）", "none", "", "x", true},
		{"cross-site 擋", "cross-site", "", "x", false},
		{"same-site 擋", "same-site", "", "x", false},
		{"無 SecFetch + 同 Origin", "", "http://127.0.0.1:8091", "127.0.0.1:8091", true},
		{"無 SecFetch + 異 Origin 擋", "", "http://evil.com", "127.0.0.1:8091", false},
		{"無 SecFetch + 無 Origin（curl 自用）", "", "", "x", true},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "http://"+c.host+"/chat", nil)
		req.Host = c.host
		if c.secFetch != "" {
			req.Header.Set("Sec-Fetch-Site", c.secFetch)
		}
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if got := sameOrigin(req); got != c.wantAllow {
			t.Errorf("%s：sameOrigin=%v，want %v", c.name, got, c.wantAllow)
		}
	}
}

// 多輪對話壓成氣泡：user 提問 + assistant 最終回覆進氣泡；tool-call turn 折成 UsedTool、tool 結果不進。
func TestToBubbles_MultiTurn(t *testing.T) {
	hist := []schema.Message{
		{Role: schema.RoleSystem, Content: "系統層（略過）"},
		{Role: schema.RoleUser, Content: "第一個任務"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "t1", Name: "bash"}}}, // 動工具
		{Role: schema.RoleUser, ToolCallID: "t1", Content: "工具結果（不進氣泡）"},
		{Role: schema.RoleAssistant, Content: "第一個任務完成"},
		{Role: schema.RoleUser, Content: "第二個任務"}, // 第二輪 user——不該被當系統提醒
		{Role: schema.RoleAssistant, Content: "第二個任務完成"},
	}
	b := toBubbles(hist)
	if len(b) != 4 {
		t.Fatalf("應 4 個氣泡（2 提問 + 2 回覆），got %d", len(b))
	}
	if !b[0].You || b[0].Text != "第一個任務" {
		t.Errorf("b[0] 應為你的提問，got %+v", b[0])
	}
	if b[1].You || b[1].Text != "第一個任務完成" || !b[1].UsedTool {
		t.Errorf("b[1] 應為 agent 回覆且標 UsedTool，got %+v", b[1])
	}
	if !b[2].You || b[2].Text != "第二個任務" {
		t.Errorf("b[2] 應為第二輪提問（非系統提醒），got %+v", b[2])
	}
	if b[3].You || b[3].Text != "第二個任務完成" || b[3].UsedTool {
		t.Errorf("b[3] 應為 agent 回覆、未動工具，got %+v", b[3])
	}
}

// 未啟用 chat（唯讀）：GET 顯示啟用說明；POST 直接 404（無寫入端點）。
func TestChat_DisabledIsReadOnly(t *testing.T) {
	srv := newServer(nil, "", "", nil)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/chat", nil))
	if !strings.Contains(rec.Body.String(), "COGITO_DASH_CHAT=1") {
		t.Error("停用時 GET /chat 應提示如何啟用")
	}

	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, httptest.NewRequest("POST", "/chat", nil))
	if rec2.Code != 404 {
		t.Errorf("停用時 POST /chat 應 404（無寫入端點），got %d", rec2.Code)
	}
}

// 啟用 chat 時，跨站 POST 必須被擋在執行 agent 之前（用零值 chatRunner，reject 路徑不碰 engine）。
func TestChatPost_RejectsCrossSite(t *testing.T) {
	srv := newServer(nil, "", "", &chatRunner{})
	req := httptest.NewRequest("POST", "/chat", strings.NewReader("msg=rm -rf"))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("跨站 POST 應 403（CSRF 擋），got %d", rec.Code)
	}
}
