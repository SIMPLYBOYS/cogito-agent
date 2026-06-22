package observability

import (
	"context"
	"log"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// PricingModel 是各模型每百萬 Token 的美元單價（輸入/輸出）。
// 數據來自 Anthropic 官方定價；切換模型時在此登記即可。
var PricingModel = map[string]struct {
	InputPrice  float64
	OutputPrice float64
}{
	"claude-fable-5":    {InputPrice: 10.0, OutputPrice: 50.0},
	"claude-opus-4-8":   {InputPrice: 5.0, OutputPrice: 25.0},
	"claude-opus-4-7":   {InputPrice: 5.0, OutputPrice: 25.0},
	"claude-sonnet-4-6": {InputPrice: 3.0, OutputPrice: 15.0},
	"claude-haiku-4-5":  {InputPrice: 1.0, OutputPrice: 5.0},
}

// CostTracker 用 decorator 模式包裹一個 LLMProvider：它本身也實現 LLMProvider 接口，
// 在轉調真實 provider 前後計時、抽取 Token 消耗、計算費用並累加進 Session。引擎對此毫不知情。
type CostTracker struct {
	nextProvider provider.LLMProvider
	modelName    string
	session      *ctxpkg.Session
}

func NewCostTracker(next provider.LLMProvider, modelName string, session *ctxpkg.Session) *CostTracker {
	return &CostTracker{
		nextProvider: next,
		modelName:    modelName,
		session:      session,
	}
}

// MaxContextTokens 透傳被包裝的 provider 的上下文窗口（decorator 不改變該屬性）。
func (t *CostTracker) MaxContextTokens() int {
	return t.nextProvider.MaxContextTokens()
}

// ModelName 透傳被包裝的 provider 的模型 id。
func (t *CostTracker) ModelName() string {
	return t.nextProvider.ModelName()
}

func (t *CostTracker) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	startTime := time.Now()

	respMsg, err := t.nextProvider.Generate(ctx, msgs, availableTools)

	latency := time.Since(startTime)

	if err != nil {
		log.Printf("[Tracker] ❌ API 調用失敗，耗時: %v\n", latency)
		return respMsg, err
	}

	if respMsg.Usage != nil {
		promptTokens := respMsg.Usage.PromptTokens
		completionTokens := respMsg.Usage.CompletionTokens

		var cost float64
		if price, exists := PricingModel[t.modelName]; exists {
			cost = (float64(promptTokens)*price.InputPrice + float64(completionTokens)*price.OutputPrice) / 1000000.0
		}

		log.Printf("[Tracker] 📊 API 調用完成 | 耗時: %v | 輸入: %d tk | 輸出: %d tk | 花費: $%.6f\n",
			latency, promptTokens, completionTokens, cost)

		if t.session != nil {
			t.session.RecordUsage(promptTokens, completionTokens, cost)
			log.Printf("[Tracker] 💰 當前會話 (%s) 累計花費: $%.6f\n", t.session.ID, t.session.TotalCostUSD)
		}
	} else {
		log.Printf("[Tracker] ⚠️ API 調用完成，但未返回 Usage 數據 | 耗時: %v\n", latency)
	}

	return respMsg, nil
}
