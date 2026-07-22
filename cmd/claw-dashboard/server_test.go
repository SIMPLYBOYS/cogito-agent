package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// in-process 測 handler（NewRequest/NewRecorder，不綁 port——沙箱下可跑，且測的是「邏輯」非「部署」）。
func TestServerRoutes_NoStore(t *testing.T) {
	srv := newServer(nil, "", "", nil)

	cases := []struct{ path, want string }{
		{"/", "cogito<em> agent</em>"},
		{"/status", "loopback"},
		{"/runs", "未設 sessions"}, // 無 store → 提示設目錄
		{"/governance", "提案佇列"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != 200 {
			t.Errorf("GET %s → %d，want 200", c.path, rec.Code)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, c.want) {
			t.Errorf("GET %s 應含 %q", c.path, c.want)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("GET %s 缺嚴格 CSP", c.path)
		}
		if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
			t.Errorf("GET %s 含外部資源連結（應自包含）", c.path)
		}
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 {
		t.Errorf("未知路徑應 404，got %d", rec.Code)
	}
}

// C1 端到端（in-process）：存一個含 spawn_subagent 的 session → /runs 列出它、/runs/{id} 渲染執行樹。
func TestServer_RunsWithRealStore(t *testing.T) {
	dir := t.TempDir()
	store, err := ctxpkg.NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap := &ctxpkg.SessionSnapshot{
		ID: "slack:C123", UpdatedAt: "2026-07-18T10:00:00Z", TotalCostUSD: 0.42,
		History: []schema.Message{
			{Role: schema.RoleUser, Content: "重構 auth 模組"},
			{Role: schema.RoleAssistant, Content: "派 reviewer", ToolCalls: []schema.ToolCall{
				{ID: "c1", Name: "spawn_subagent", Arguments: json.RawMessage(`{"agent_type":"code-reviewer"}`)}}},
			{Role: schema.RoleUser, ToolCallID: "c1", Content: "審查完成：OK"},
			{Role: schema.RoleAssistant, Content: "搞定"},
		},
	}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	srv := newServer(store, dir, dir, nil)

	// /runs 列表：含 session id、任務、subagent badge
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/runs", nil))
	list := rec.Body.String()
	for _, w := range []string{"slack:C123", "重構 auth", "subagent"} {
		if !strings.Contains(list, w) {
			t.Errorf("/runs 應含 %q", w)
		}
	}

	// /runs/{id}：渲染執行樹（主 agent + 子 agent 委派 + 報告 + 最終回答）
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, httptest.NewRequest("GET", "/runs/"+url.PathEscape("slack:C123"), nil))
	if rec2.Code != 200 {
		t.Fatalf("/runs/{id} → %d", rec2.Code)
	}
	detail := rec2.Body.String()
	for _, w := range []string{"委派 1 個", "code-reviewer", "審查完成", "搞定"} {
		if !strings.Contains(detail, w) {
			t.Errorf("/runs/{id} 執行樹應含 %q", w)
		}
	}
}
