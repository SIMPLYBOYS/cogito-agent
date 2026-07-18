package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// governance 放行動作：跨站被擋；無提案給 flash；技能名路徑穿越被守衛擋。
func TestGovActions_CSRFAndGuards(t *testing.T) {
	ws := t.TempDir()
	srv := newServer(nil, "", ws, nil)

	post := func(path, body, secFetch string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if secFetch != "" {
			req.Header.Set("Sec-Fetch-Site", secFetch)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	govBody := func() string {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", "/governance", nil))
		return rec.Body.String()
	}

	// 跨站 → 403
	if c := post("/governance/apply-config", "", "cross-site"); c != 403 {
		t.Fatalf("跨站 apply-config 應 403，got %d", c)
	}

	// 同源、無提案 → 303 + flash
	if c := post("/governance/apply-config", "", "same-origin"); c != 303 {
		t.Fatalf("同源 apply-config 應 303，got %d", c)
	}
	if !strings.Contains(govBody(), "無調參提案可套用") {
		t.Error("無提案時應 flash 提示")
	}

	// 路徑穿越的技能名 → 擋（flash 無效）
	if c := post("/governance/promote-skill", "name="+url.QueryEscape("../evil"), "same-origin"); c != 303 {
		t.Fatalf("promote-skill 應 303，got %d", c)
	}
	if !strings.Contains(govBody(), "無效的技能名") {
		t.Error("路徑穿越的技能名應被守衛擋下")
	}
}
