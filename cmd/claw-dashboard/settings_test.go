package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// POST /env-config：跨站被擋；同源則寫 .env（祕密逐字保留、toggle 寫 1），CSRF 生效。
func TestEnvConfigSave_WritesEnvPreservesSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // 讓 handler 的 ".env" 解到 temp
	t.Cleanup(func() {
		for _, e := range editableEnv {
			os.Unsetenv(e.Key) // 清掉 handler os.Setenv 的殘留，避免污染其他測試
		}
	})
	if err := os.WriteFile(".env", []byte("ANTHROPIC_API_KEY=sk-LIVE-SECRET\nCLAUDE_MODEL=claude-opus-4-8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newServer(nil, "", dir, nil)

	// 跨站 → 403（不寫）
	req := httptest.NewRequest("POST", "/env-config", strings.NewReader("CLAUDE_MODEL=hacked"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("跨站 /env-config 應 403，got %d", rec.Code)
	}

	// 同源 → 303 + 寫入
	req2 := httptest.NewRequest("POST", "/env-config", strings.NewReader("CLAUDE_MODEL=claude-haiku-4-5&COGITO_MEMORY_SYNTH=1&COGITO_PROVIDER=claude"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("Sec-Fetch-Site", "same-origin")
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != 303 {
		t.Fatalf("同源 /env-config 應 303，got %d", rec2.Code)
	}
	got, _ := os.ReadFile(".env")
	s := string(got)
	if !strings.Contains(s, "ANTHROPIC_API_KEY=sk-LIVE-SECRET") {
		t.Error("祕密行被動到了！")
	}
	if !strings.Contains(s, "CLAUDE_MODEL=claude-haiku-4-5") {
		t.Error("CLAUDE_MODEL 沒更新")
	}
	if !strings.Contains(s, "COGITO_MEMORY_SYNTH=1") {
		t.Error("toggle 沒寫入")
	}
}
