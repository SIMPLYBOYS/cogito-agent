package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
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

// SSE hub 緩衝：push 後 since 取得新事件、begin 清空、end 收 running。
func TestSSEHub_SinceBuffers(t *testing.T) {
	h := &sseHub{}
	h.begin()
	if !h.isRunning() { t.Fatal("begin 後應 running") }
	h.push(evJSON("tool", "bash"))
	h.push(evJSON("msg", "done"))
	evs, running, total := h.since(0)
	if len(evs) != 2 || total != 2 || !running { t.Fatalf("since(0)：evs=%d total=%d running=%v", len(evs), total, running) }
	evs2, _, _ := h.since(2)
	if len(evs2) != 0 { t.Errorf("since(total) 應無新事件，got %d", len(evs2)) }
	h.end()
	if h.isRunning() { t.Error("end 後不應 running") }
	// begin 重置緩衝
	h.begin()
	if _, _, total := h.since(0); total != 0 { t.Errorf("begin 應清空緩衝，got total=%d", total) }
}

// 執行中：chat 頁應含串流區 #live + 載入 /chat.js，且 CSP 放寬到 script-src 'self'。
func TestChatGet_RunningShowsStream(t *testing.T) {
	cr := &chatRunner{hub: &sseHub{}}
	cr.hub.begin() // 標記 running
	srv := newServer(nil, "", "", cr)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/chat", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `id="live"`) || !strings.Contains(body, "/chat.js") {
		t.Error("執行中應渲染串流區 #live 並載入 /chat.js")
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Error("chat 頁 CSP 應放寬 script-src 'self'（供 SSE）")
	}
	if !strings.Contains(body, "disabled") {
		t.Error("執行中輸入框應 disabled，防重複交辦")
	}
}

// /chat.js 服務串流客戶端。
func TestChatJS_Served(t *testing.T) {
	srv := newServer(nil, "", "", &chatRunner{hub: &sseHub{}})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/chat.js", nil))
	if !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Error("/chat.js 應回 javascript content-type")
	}
	if !strings.Contains(rec.Body.String(), "EventSource") {
		t.Error("/chat.js 應含 EventSource 客戶端")
	}
}

// POST /chat/reset 清空 operator session（新對話）；跨站被 CSRF 擋。
func TestChatReset_ClearsSession(t *testing.T) {
	dir := t.TempDir()
	store, _ := ctxpkg.NewFileSessionStore(dir)
	ctxpkg.GlobalSessionMgr.SetStore(store)
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(operatorSessionID, dir)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "污染歷史"})
	srv := newServer(store, dir, dir, &chatRunner{hub: &sseHub{}, workDir: dir})

	req := httptest.NewRequest("POST", "/chat/reset", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 303 {
		t.Fatalf("reset → %d，want 303", rec.Code)
	}
	if sess.HistoryLen() != 0 {
		t.Error("reset 後 operator session 應清空")
	}

	req2 := httptest.NewRequest("POST", "/chat/reset", nil)
	req2.Header.Set("Sec-Fetch-Site", "cross-site")
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != 403 {
		t.Errorf("跨站 reset 應 403，got %d", rec2.Code)
	}
}
