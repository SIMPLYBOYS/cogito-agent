package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

func TestPlatformOf(t *testing.T) {
	cases := map[string]string{
		"slack:C123":     "Slack",
		"telegram:99":    "Telegram",
		"operator":       "Operator（dashboard）",
		"cli-session":    "本機／CLI",
		"discord:x":      "discord", // 未知前綴原樣
	}
	for id, want := range cases {
		if got := platformOf(id); got != want {
			t.Errorf("platformOf(%q)=%q，want %q", id, got, want)
		}
	}
}

// /metrics 聚合：加總花費/token，並按平台、模型切片渲染。
func TestMetrics_Aggregates(t *testing.T) {
	dir := t.TempDir()
	store, _ := ctxpkg.NewFileSessionStore(dir)
	seed := []*ctxpkg.SessionSnapshot{
		{ID: "slack:A", Model: "claude-opus-4-8", TotalCostUSD: 1.0, TotalPromptTokens: 100, TotalCompletionTokens: 50},
		{ID: "slack:B", Model: "claude-opus-4-8", TotalCostUSD: 0.5, TotalPromptTokens: 40, TotalCompletionTokens: 10},
		{ID: "telegram:C", Model: "claude-haiku-4-5", TotalCostUSD: 0.25, TotalPromptTokens: 20, TotalCompletionTokens: 5},
	}
	for _, s := range seed {
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	srv := newServer(store, dir, dir, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("/metrics → %d", rec.Code)
	}
	for _, want := range []string{
		"$1.7500",       // 總花費 1.0+0.5+0.25
		"Slack",         // 平台切片
		"Telegram",
		"claude-opus-4-8",
		"bfill",         // 長條有渲染
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics 應含 %q", want)
		}
	}
	// Slack 花費(1.5) > Telegram(0.25)，Slack 應排在前
	if strings.Index(body, "Slack") > strings.Index(body, "Telegram") {
		t.Error("平台應按花費新→舊排（Slack 在 Telegram 前）")
	}
}
