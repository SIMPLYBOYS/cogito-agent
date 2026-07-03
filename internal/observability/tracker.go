package observability

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"
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

// fallbackInputPrice/fallbackOutputPrice：模型不在 PricingModel 時的保守估價（每百萬 token USD）。
// 沒有這層，未登記模型（OpenAI 相容端點等）的 cost 會靜默為 0，讓成本熔斷（MaxCostUSD）完全失效。
// 預設取 opus 級單價（偏高＝寧可提早熔斷也不超支）；接便宜模型時用 COGITO_PRICE_* 覆蓋為實際值。
var (
	fallbackInputPrice  = envFloatOr("COGITO_PRICE_INPUT_USD", 5.0)
	fallbackOutputPrice = envFloatOr("COGITO_PRICE_OUTPUT_USD", 25.0)
	warnedModels        sync.Map // 每個未登記 model 只警告一次，避免每回合刷 log
)

func envFloatOr(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
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
		cacheRead := respMsg.Usage.CacheReadTokens
		cacheCreation := respMsg.Usage.CacheCreationTokens

		price, exists := PricingModel[t.modelName]
		if !exists {
			// 未登記模型：改用 fallback 估價，讓成本熔斷仍生效（而非靜默 0）。每個 model 只警告一次。
			price.InputPrice, price.OutputPrice = fallbackInputPrice, fallbackOutputPrice
			if _, dup := warnedModels.LoadOrStore(t.modelName, true); !dup {
				log.Printf("[Tracker] ⚠️ 模型 %q 未登記定價，改用 fallback 估價（in $%.1f / out $%.1f 每百萬 tk）；如需精確請在 PricingModel 登記或設 COGITO_PRICE_INPUT_USD/COGITO_PRICE_OUTPUT_USD。\n", t.modelName, fallbackInputPrice, fallbackOutputPrice)
			}
		}
		// Anthropic 快取計費：命中讀取約 0.1x、寫入約 1.25x 基礎輸入價（promptTokens 已不含快取讀取）。
		cost := (float64(promptTokens)*price.InputPrice +
			float64(cacheRead)*price.InputPrice*0.1 +
			float64(cacheCreation)*price.InputPrice*1.25 +
			float64(completionTokens)*price.OutputPrice) / 1000000.0

		log.Printf("[Tracker] 📊 API 調用完成 | 耗時: %v | 輸入: %d tk (快取讀 %d / 寫 %d) | 輸出: %d tk | 花費: $%.6f\n",
			latency, promptTokens, cacheRead, cacheCreation, completionTokens, cost)

		if t.session != nil {
			t.session.RecordUsage(promptTokens, completionTokens, cost)
			log.Printf("[Tracker] 💰 當前會話 (%s) 累計花費: $%.6f\n", t.session.ID, t.session.TotalCostUSD)
		}
	} else {
		log.Printf("[Tracker] ⚠️ API 調用完成，但未返回 Usage 數據 | 耗時: %v\n", latency)
	}

	return respMsg, nil
}
