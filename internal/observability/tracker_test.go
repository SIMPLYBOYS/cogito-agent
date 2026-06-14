package observability

import (
	"context"
	"testing"

	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/schema"
)

// stubProvider 返回固定 Usage 的假 provider，用于离线验证计费逻辑（不打真实 API）。
type stubProvider struct{ prompt, completion int }

func (s *stubProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "ok",
		Usage:   &schema.Usage{PromptTokens: s.prompt, CompletionTokens: s.completion},
	}, nil
}

func approxEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// 验证 ch22 engine factory 的核心保证：每个会话各记各的账，互不污染。
func TestCostTracker_PerSessionAccounting(t *testing.T) {
	ctx := context.Background()
	stub := &stubProvider{prompt: 1000, completion: 2000}

	sessA := ctxpkg.NewSession("chA", "/tmp")
	sessB := ctxpkg.NewSession("chB", "/tmp")
	trackerA := NewCostTracker(stub, "claude-opus-4-8", sessA)
	trackerB := NewCostTracker(stub, "claude-opus-4-8", sessB)

	// A 调两次，B 调一次
	_, _ = trackerA.Generate(ctx, nil, nil)
	_, _ = trackerA.Generate(ctx, nil, nil)
	_, _ = trackerB.Generate(ctx, nil, nil)

	// opus-4-8: $5/1M 输入, $25/1M 输出 → 单次 = (1000*5 + 2000*25)/1e6 = 0.055
	const perCall = (1000*5.0 + 2000*25.0) / 1_000_000.0

	if sessA.TotalPromptTokens != 2000 || sessA.TotalCompletionTokens != 4000 {
		t.Errorf("A token 累计错: prompt=%d completion=%d", sessA.TotalPromptTokens, sessA.TotalCompletionTokens)
	}
	if !approxEq(sessA.TotalCostUSD, 2*perCall) {
		t.Errorf("A 成本错: got %.6f want %.6f", sessA.TotalCostUSD, 2*perCall)
	}
	// B 必须独立，不受 A 的两次调用影响
	if sessB.TotalPromptTokens != 1000 || !approxEq(sessB.TotalCostUSD, perCall) {
		t.Errorf("B 应独立计费(隔离): tokens=%d cost=%.6f", sessB.TotalPromptTokens, sessB.TotalCostUSD)
	}
}

// 未知模型无定价 → 成本 0，但 token 仍应如实累计。
func TestCostTracker_UnknownModelZeroCost(t *testing.T) {
	sess := ctxpkg.NewSession("x", "/tmp")
	tr := NewCostTracker(&stubProvider{prompt: 100, completion: 100}, "unknown-model", sess)
	_, _ = tr.Generate(context.Background(), nil, nil)

	if sess.TotalCostUSD != 0 {
		t.Errorf("未知模型应 0 成本，got %.6f", sess.TotalCostUSD)
	}
	if sess.TotalPromptTokens != 100 {
		t.Errorf("token 仍应累计，got %d", sess.TotalPromptTokens)
	}
}
