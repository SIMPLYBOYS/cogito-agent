package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// in-process 測 handler（NewRequest/NewRecorder，不綁 port——沙箱下可跑，且測的是「邏輯」非「部署」）。
func TestServerRoutes(t *testing.T) {
	srv := newServer()

	cases := []struct {
		path, want string
	}{
		{"/", "cogito ops"},          // 首頁含站名
		{"/status", "loopback"},      // status 頁誠實標示存取模式
		{"/runs", "C1"},              // stub 誠實標示未實作 phase
		{"/governance", "留 chat"},   // 治理檢視標示動作留 chat
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
		// 每頁都要有嚴格 CSP（面板是 attack surface）
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("GET %s 缺嚴格 CSP，got %q", c.path, csp)
		}
		// 自包含：不得有外部資源（http:// 連結）
		if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
			t.Errorf("GET %s 含外部資源連結（應自包含）", c.path)
		}
	}

	// 未知路徑 → 404
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 {
		t.Errorf("未知路徑應 404，got %d", rec.Code)
	}
}
