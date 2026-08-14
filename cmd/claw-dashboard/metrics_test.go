package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func TestPlatformOf(t *testing.T) {
	cases := map[string]string{
		"slack:C123":  "Slack",
		"telegram:99": "Telegram",
		"operator":    "Operator（dashboard）",
		"cli-session": "本機／CLI",
		"discord:x":   "discord", // 未知前綴原樣
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
		"$1.7500", // 總花費 1.0+0.5+0.25
		"Slack",   // 平台切片
		"Telegram",
		"claude-opus-4-8",
		"bfill", // 長條有渲染
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

// 快取與耗時要從逐則 Usage 彙總，不是從 snapshot 的總計欄位。
//
// 這一條同時守著本次改動的價值主張：快取 token 一直都有落盤，只是沒人加總——所以它對
// 【既有】session 就該立刻有數字，不需要等新資料。若哪天有人把這段迴圈「優化」成只讀
// snapshot 總計，畫面會安靜地變成 0，而沒有任何東西會壞掉。
func TestMetrics_AggregatesCacheAndLatencyFromHistory(t *testing.T) {
	dir := t.TempDir()
	st, err := ctxpkg.NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Save(&ctxpkg.SessionSnapshot{
		ID: "telegram:1", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
		TotalPromptTokens: 100, TotalCompletionTokens: 20, TotalCostUSD: 0.5,
		History: []schema.Message{
			{Role: schema.RoleAssistant, Usage: &schema.Usage{
				PromptTokens: 60, CacheReadTokens: 900, CacheCreationTokens: 40, LatencyMS: 1000}},
			{Role: schema.RoleAssistant, Usage: &schema.Usage{
				PromptTokens: 40, CacheReadTokens: 100, CacheCreationTokens: 10, LatencyMS: 3000}},
			{Role: schema.RoleUser, Content: "沒有 usage 的訊息不能讓彙總崩掉"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	srv := newServer(st, dir, t.TempDir(), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		"1000", // 快取讀總量 900+100
		"50",   // 快取寫總量 40+10
		"90%",  // 命中率 1000/(100+1000)
		"3000", // p95 耗時
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics 頁少了 %q——快取/耗時沒有從 history 彙總", want)
		}
	}
}
