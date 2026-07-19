package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func revealReq(srv http.Handler, key, secFetch string) (int, string) {
	req := httptest.NewRequest("GET", "/secret/reveal?key="+url.QueryEscape(key), nil)
	if secFetch != "" {
		req.Header.Set("Sec-Fetch-Site", secFetch)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// 眼睛顯示：同源+已知 key 回值；跨站 403；未知 key 404；INSECURE（非 loopback）一律 403。
func TestSecretReveal(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-SECRET-123")
	t.Setenv("COGITO_DASH_INSECURE", "")
	srv := newServer(nil, "", t.TempDir(), nil)

	if code, body := revealReq(srv, "ANTHROPIC_API_KEY", "same-origin"); code != 200 || body != "sk-SECRET-123" {
		t.Errorf("同源已知 key 應回值，got %d %q", code, body)
	}
	if code, _ := revealReq(srv, "ANTHROPIC_API_KEY", "cross-site"); code != 403 {
		t.Errorf("跨站應 403，got %d", code)
	}
	if code, _ := revealReq(srv, "COGITO_ALLOWED_USERS", "same-origin"); code != 404 {
		t.Errorf("非祕密 key 應 404，got %d", code)
	}
	t.Setenv("COGITO_DASH_INSECURE", "1") // 非 loopback → 硬性拒絕
	if code, _ := revealReq(srv, "ANTHROPIC_API_KEY", "same-origin"); code != 403 {
		t.Errorf("INSECURE 部署應拒絕顯示祕密，got %d", code)
	}
}

// 輪替：同源寫 .env（其他 key 逐字保留）；跨站 403；未知 key 400；INSECURE 403。
func TestSecretSave(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("COGITO_DASH_INSECURE", "")
	t.Cleanup(func() {
		for _, k := range secretKeys {
			os.Unsetenv(k)
		}
	})
	if err := os.WriteFile(".env", []byte("ANTHROPIC_API_KEY=old-key\nCOGITO_ALLOWED_USERS=alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newServer(nil, "", dir, nil)
	save := func(body, secFetch string) int {
		req := httptest.NewRequest("POST", "/secret", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if secFetch != "" {
			req.Header.Set("Sec-Fetch-Site", secFetch)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	if save("key=ANTHROPIC_API_KEY&value=sk-NEW", "cross-site") != 403 {
		t.Error("跨站輪替應 403")
	}
	if save("key=NOT_SECRET&value=x", "same-origin") != 400 {
		t.Error("未知祕密 key 應 400")
	}
	if code := save("key=ANTHROPIC_API_KEY&value=sk-NEW", "same-origin"); code != 303 {
		t.Fatalf("同源輪替應 303，got %d", code)
	}
	s := readFileStr(t, ".env")
	if !strings.Contains(s, "ANTHROPIC_API_KEY=sk-NEW") {
		t.Error("金鑰沒輪替")
	}
	if !strings.Contains(s, "COGITO_ALLOWED_USERS=alice") {
		t.Error("其他 key 被動到了")
	}

	t.Setenv("COGITO_DASH_INSECURE", "1")
	if save("key=ANTHROPIC_API_KEY&value=x", "same-origin") != 403 {
		t.Error("INSECURE 部署應拒絕輪替祕密")
	}
}
