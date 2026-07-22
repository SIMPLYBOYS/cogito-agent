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

// TestServer_RunsLive 驗證執行中 session 的即時視圖：詳情頁放寬 CSP＋掛輪詢腳本，
// fragment 端點回 run-tree 片段＋X-Run-Running 標頭；非執行中則退回靜態（無腳本）。
func TestServer_RunsLive(t *testing.T) {
	dir := t.TempDir()
	store, err := ctxpkg.NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap := &ctxpkg.SessionSnapshot{
		ID: "telegram_42", Running: true,
		History: []schema.Message{
			{Role: schema.RoleUser, Content: "查一下狀態"},
			{Role: schema.RoleAssistant, Content: "查詢中", ToolCalls: []schema.ToolCall{
				{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)}}},
		},
	}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	srv := newServer(store, dir, dir, nil)
	id := url.PathEscape("telegram_42")

	// 執行中詳情頁：放寬 CSP（script-src 'self'）＋輪詢腳本＋LIVE 橫幅
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/runs/"+id, nil))
	body := rec.Body.String()
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("執行中詳情頁應放寬 CSP，得到 %q", csp)
	}
	for _, w := range []string{"/runs.js", "livebanner", `id="runtree"`} {
		if !strings.Contains(body, w) {
			t.Errorf("執行中詳情頁應含 %q", w)
		}
	}

	// fragment 端點：X-Run-Running: 1 ＋回 run-tree 片段（不含版面 nav）
	recF := httptest.NewRecorder()
	srv.ServeHTTP(recF, httptest.NewRequest("GET", "/runs/"+id+"/fragment", nil))
	if got := recF.Header().Get("X-Run-Running"); got != "1" {
		t.Errorf("執行中 fragment 應回 X-Run-Running: 1，得到 %q", got)
	}
	frag := recF.Body.String()
	if !strings.Contains(frag, "查一下狀態") {
		t.Error("fragment 應含 run-tree 內容")
	}
	if strings.Contains(frag, "<nav") || strings.Contains(frag, "/metrics") {
		t.Error("fragment 應只回片段、不含版面 nav")
	}

	// 非執行中：退回靜態（嚴 CSP、無輪詢腳本），fragment 回 X-Run-Running: 0
	snap.Running = false
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	recS := httptest.NewRecorder()
	srv.ServeHTTP(recS, httptest.NewRequest("GET", "/runs/"+id, nil))
	if strings.Contains(recS.Body.String(), "/runs.js") {
		t.Error("非執行中詳情頁不應掛輪詢腳本")
	}
	if csp := recS.Header().Get("Content-Security-Policy"); strings.Contains(csp, "script-src") {
		t.Errorf("非執行中詳情頁應為嚴 CSP，得到 %q", csp)
	}
	recF0 := httptest.NewRecorder()
	srv.ServeHTTP(recF0, httptest.NewRequest("GET", "/runs/"+id+"/fragment", nil))
	if got := recF0.Header().Get("X-Run-Running"); got != "0" {
		t.Errorf("非執行中 fragment 應回 X-Run-Running: 0，得到 %q", got)
	}
}
