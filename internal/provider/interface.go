// internal/provider/interface.go
package provider

import (
	"context"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// LLMProvider defines the unified interface for communicating with large models
type LLMProvider interface {
	// Generate receives the current context history and available tools list, returns the model response
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
	// MaxContextTokens 返回該模型的上下文窗口大小（token）。供自適應壓縮按真實窗口設定水位線
	//（不同模型差異很大：Claude 200k、Gemini 1M、本地 Llama 可能僅 8k）。
	MaxContextTokens() int
	// ModelName 返回模型 id（如 claude-opus-4-8），供 OTel gen_ai.request.model 與成本計算使用。
	ModelName() string
}

// Configurable 是【可選】介面：回傳一個換了模型/輸出上限的 provider 變體（原 provider 不變）。
// 供具名子 agent 選模型（model）與 effort（→輸出 token 上限）。model 空＝保留原模型；maxTokens<=0＝保留原上限。
// provider 未實作此介面時，子 agent 沿用主引擎的 provider（model/effort 靜默忽略）。
type Configurable interface {
	Configure(model string, maxTokens int) LLMProvider
}
