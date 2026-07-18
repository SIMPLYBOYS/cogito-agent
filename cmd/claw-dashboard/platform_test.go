package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// 安全底線：平台頁顯示金鑰「有無」但絕不露值；provider 解析要與 provider.FromEnv 一致。
func TestPlatform_MasksSecretsAndResolvesProvider(t *testing.T) {
	const secret = "sk-super-secret-value-DO-NOT-LEAK"
	t.Setenv("COGITO_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", secret)
	t.Setenv("OPENAI_MODEL", "qwen2.5-72b")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:telegram-secret")

	srv := newServer(nil, "", "", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/platform", nil))
	body := rec.Body.String()

	if rec.Code != 200 {
		t.Fatalf("/platform → %d", rec.Code)
	}
	if strings.Contains(body, secret) || strings.Contains(body, "telegram-secret") {
		t.Fatal("祕密值外洩到頁面——安全底線破了")
	}
	for _, want := range []string{"OpenAI 相容", "qwen2.5-72b", "已設定 ✓", "已綁定"} {
		if !strings.Contains(body, want) {
			t.Errorf("平台頁應含 %q", want)
		}
	}
}

// 未設 COGITO_PROVIDER → 預設 Claude；未設金鑰 → 標未設，不誤報已設定。
func TestPlatform_DefaultClaudeAndMissingKey(t *testing.T) {
	t.Setenv("COGITO_PROVIDER", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	srv := newServer(nil, "", "", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/platform", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "Claude") {
		t.Error("未設 COGITO_PROVIDER 應回退顯示 Claude")
	}
	if !strings.Contains(body, "未設 —") {
		t.Error("未設 ANTHROPIC_API_KEY 應標「未設 —」")
	}
}
