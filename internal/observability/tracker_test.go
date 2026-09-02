package observability

import (
	"context"
	"testing"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// stubProvider 回傳固定 Usage 的假 provider，用於離線驗證計費邏輯（不打真實 API）。
type stubProvider struct{ prompt, completion int }

func (s *stubProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "ok",
		Usage:   &schema.Usage{PromptTokens: s.prompt, CompletionTokens: s.completion},
	}, nil
}

func (s *stubProvider) MaxContextTokens() int { return 200000 }
func (s *stubProvider) ModelName() string     { return "stub-model" }

func approxEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// 驗證 engine factory 的核心保證：每個會話各記各的賬，互不汙染。
func TestCostTracker_PerSessionAccounting(t *testing.T) {
	ctx := context.Background()
	stub := &stubProvider{prompt: 1000, completion: 2000}

	sessA := ctxpkg.NewSession("chA", "/tmp")
	sessB := ctxpkg.NewSession("chB", "/tmp")
	trackerA := NewCostTracker(stub, "claude-opus-4-8", sessA)
	trackerB := NewCostTracker(stub, "claude-opus-4-8", sessB)

	// A 調兩次，B 調一次
	_, _ = trackerA.Generate(ctx, nil, nil)
	_, _ = trackerA.Generate(ctx, nil, nil)
	_, _ = trackerB.Generate(ctx, nil, nil)

	// opus-4-8: $5/1M 輸入, $25/1M 輸出 → 單次 = (1000*5 + 2000*25)/1e6 = 0.055
	const perCall = (1000*5.0 + 2000*25.0) / 1_000_000.0

	if sessA.TotalPromptTokens != 2000 || sessA.TotalCompletionTokens != 4000 {
		t.Errorf("A token 累計錯: prompt=%d completion=%d", sessA.TotalPromptTokens, sessA.TotalCompletionTokens)
	}
	if !approxEq(sessA.TotalCostUSD, 2*perCall) {
		t.Errorf("A 成本錯: got %.6f want %.6f", sessA.TotalCostUSD, 2*perCall)
	}
	// B 必須獨立，不受 A 的兩次呼叫影響
	if sessB.TotalPromptTokens != 1000 || !approxEq(sessB.TotalCostUSD, perCall) {
		t.Errorf("B 應獨立計費(隔離): tokens=%d cost=%.6f", sessB.TotalPromptTokens, sessB.TotalCostUSD)
	}
}

// 未知模型（如 OpenAI 相容端點）改用 fallback 估價 → 成本【非 0】，成本熔斷才不會失效。
// 這是安全修補：舊行為靜默 0 會讓 MaxCostUSD 熔斷對未登記模型完全失效。
func TestCostTracker_UnknownModelUsesFallbackPrice(t *testing.T) {
	sess := ctxpkg.NewSession("x", "/tmp")
	tr := NewCostTracker(&stubProvider{prompt: 100, completion: 100}, "unknown-model", sess)
	_, _ = tr.Generate(context.Background(), nil, nil)

	// fallback 預設 in $5 / out $25 每百萬 → (100*5 + 100*25)/1e6 = 0.003
	want := (100*fallbackInputPrice + 100*fallbackOutputPrice) / 1_000_000.0
	if !approxEq(sess.TotalCostUSD, want) {
		t.Errorf("未知模型應以 fallback 計費 %.6f，got %.6f", want, sess.TotalCostUSD)
	}
	if sess.TotalCostUSD == 0 {
		t.Error("未知模型成本不得為 0——否則成本熔斷失效")
	}
	if sess.TotalPromptTokens != 100 {
		t.Errorf("token 仍應累計，got %d", sess.TotalPromptTokens)
	}
}

// 耗時要跟著訊息落盤，不能只印進 log。
//
// 為什麼值得一條測試：CostTracker 一直都量了耗時，但只 log.Printf 就丟掉。於是
// 「哪一輪突然變慢」在面板、replay、session 檔上全都查不到——資料量得到卻救不回來，
// 是最容易長期沒人發現的那種缺失（沒有人會為「看不到的東西」開 bug）。
func TestCostTracker_LatencyPersistedInUsage(t *testing.T) {
	tr := &CostTracker{modelName: "claude-opus-4-8"} // session 為 nil：account 有防護
	msg := &schema.Message{Role: schema.RoleAssistant,
		Usage: &schema.Usage{PromptTokens: 10, CompletionTokens: 5}}

	tr.account(msg, 1500*time.Millisecond)

	if msg.Usage.LatencyMS != 1500 {
		t.Errorf("耗時應寫進 Usage（1500ms），實際 %d", msg.Usage.LatencyMS)
	}
}

// 沒有 Usage 的回應不能讓 account 崩掉（provider 沒回 usage 是既有的合法情況）。
func TestCostTracker_NoUsageDoesNotPanic(t *testing.T) {
	tr := &CostTracker{modelName: "claude-opus-4-8"}
	tr.account(&schema.Message{Role: schema.RoleAssistant}, time.Second)
}

// 計價表漏登記＝那個模型的花費是【估的】（fallback 預設 opus 級單價）。實際踩到：
// persona 把老徐設成 claude-opus-5，而表裡只有 opus-4-8/4-7——花費碰巧接近正確，
// 但那是運氣不是機制；換成便宜模型就會高估五倍，而卡片上看起來跟真的一樣。
func TestPricingCoversCurrentModels(t *testing.T) {
	for _, m := range []string{
		"claude-fable-5", "claude-mythos-5", "claude-opus-5", "claude-opus-4-8",
		"claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-5", "claude-sonnet-4-6",
		"claude-haiku-4-5",
	} {
		if !IsRegistered(m) {
			t.Errorf("%s 沒登記定價，它的花費會是 fallback 估價", m)
		}
	}
	if IsRegistered("某個真的沒登記的模型") {
		t.Error("沒登記的不該被當成有登記")
	}
}

// 帶日期尾綴的 id（API 常見）要對得上同一筆單價——否則 haiku 會被當 opus 記帳、
// 貴五倍，成本熔斷提早觸發。
func TestPricingIgnoresDateSuffix(t *testing.T) {
	if !IsRegistered("claude-haiku-4-5-20251001") {
		t.Fatal("帶日期尾綴的 id 應對得上計價表")
	}
	u := schema.Usage{PromptTokens: 1_000_000}
	plain, dated := CostOf("claude-haiku-4-5", u), CostOf("claude-haiku-4-5-20251001", u)
	if plain != dated {
		t.Errorf("同一個模型兩種寫法算出不同的錢：$%.4f vs $%.4f", plain, dated)
	}
	if plain != 1.0 {
		t.Errorf("haiku 每百萬輸入 tk 應為 $1.00，得到 $%.4f（走了 fallback？）", plain)
	}
}
