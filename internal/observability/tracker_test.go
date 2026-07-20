package observability

import (
	"context"
	"testing"

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
