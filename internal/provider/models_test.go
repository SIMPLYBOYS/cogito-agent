package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// seedWindows 直接種快取（不碰網路）：這裡要驗的是查表與退讓邏輯，不是 HTTP。
func seedWindows(t *testing.T, m map[string]int) {
	t.Helper()
	windowsMu.Lock()
	windows, windowsAt = m, time.Now()
	windowsMu.Unlock()
	t.Cleanup(func() {
		windowsMu.Lock()
		windows, windowsAt = nil, time.Time{}
		windowsMu.Unlock()
	})
}

// 窗口要用【該模型實際的】值。先前寫死 200k，於是 1M 的模型在實際容量的 15% 就開始壓縮。
func TestMaxContextTokensUsesRealWindow(t *testing.T) {
	seedWindows(t, map[string]int{
		"claude-opus-5":             1_000_000,
		"claude-haiku-4-5-20251001": 200_000,
		"claude-haiku-4-5":          200_000, // 官方 id 帶日期，剝除版一併登記
	})
	for _, c := range []struct {
		model string
		want  int
	}{
		{"claude-opus-5", 1_000_000},
		{"claude-haiku-4-5-20251001", 200_000},
		{"claude-haiku-4-5", 200_000},         // 使用者設不帶日期的寫法
		{"claude-opus-5-20260724", 1_000_000}, // 反向：使用者設帶日期的、表裡是不帶的
	} {
		p := &ClaudeProvider{model: c.model}
		if got := p.MaxContextTokens(); got != c.want {
			t.Errorf("%s 的窗口 = %d，want %d", c.model, got, c.want)
		}
	}
}

// 查不到就往【低】猜：猜低只是提早壓縮（浪費），猜高會讓任務超出窗口被 API 拒絕（失敗）。
// 方向錯了比數字錯了嚴重。
func TestMaxContextTokensFallsBackConservatively(t *testing.T) {
	seedWindows(t, map[string]int{"claude-opus-5": 1_000_000})
	p := &ClaudeProvider{model: "某個沒查到的模型"}
	if got := p.MaxContextTokens(); got != windowFallback {
		t.Errorf("查不到時應退回保守的 %d，got %d", windowFallback, got)
	}
	if windowFallback > 200_000 {
		t.Errorf("保守值不該高於最小的已知窗口（200k），現在是 %d", windowFallback)
	}
}

// 從【官方回應形狀】一路驗到快取：max_input_tokens 要變成 Window，日期尾綴要雙向查得到。
// 前面那些測試直接種快取，走不到這條路——於是「ListModels 把窗口丟掉」不會被抓到。
func TestListModelsCarriesWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("路徑錯誤: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"type":"model","id":"claude-opus-5","display_name":"Claude Opus 5",
			 "created_at":"2026-07-24T00:00:00Z","max_input_tokens":1000000,"max_tokens":128000},
			{"type":"model","id":"claude-haiku-4-5-20251001","display_name":"Claude Haiku 4.5",
			 "created_at":"2025-10-15T00:00:00Z","max_input_tokens":200000,"max_tokens":64000}
		],"has_more":false}`))
	}))
	defer srv.Close()

	p := &ClaudeProvider{client: anthropic.NewClient(
		option.WithAPIKey("test"), option.WithBaseURL(srv.URL))}
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("要 2 個模型，得到 %d", len(got))
	}
	if got[0].ID != "claude-opus-5" || got[0].Name != "Claude Opus 5" || got[0].Window != 1_000_000 {
		t.Errorf("欄位沒對上: %+v", got[0])
	}

	// 走完整條路。契約是【立即回應、稍後變準】：MaxContextTokens 絕不阻塞
	// （它被每個任務的建引擎同步呼叫），所以第一次拿到的是保守值，背景抓完才是真的。
	windowsMu.Lock()
	windows, windowsAt, windowsLoading = nil, time.Time{}, false
	windowsMu.Unlock()
	t.Cleanup(func() {
		windowsMu.Lock()
		windows, windowsAt, windowsLoading = nil, time.Time{}, false
		windowsMu.Unlock()
	})
	p.model = "claude-opus-5"
	if w := p.MaxContextTokens(); w != windowFallback {
		t.Errorf("第一次應立即回保守值（不阻塞等網路），got %d", w)
	}
	// 背景抓完後才是真的窗口。輪詢而不是固定 sleep：慢機器上不該偽陰性。
	deadline := time.Now().Add(5 * time.Second)
	for p.MaxContextTokens() != 1_000_000 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if w := p.MaxContextTokens(); w != 1_000_000 {
		t.Errorf("背景抓完後窗口 = %d，want 1000000（ListModels 把 max_input_tokens 丟掉了？）", w)
	}
	p.model = "claude-haiku-4-5" // 不帶日期的寫法也要查得到
	if w := p.MaxContextTokens(); w != 200_000 {
		t.Errorf("剝除日期後的 id 應查得到，got %d", w)
	}
}
