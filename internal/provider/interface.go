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
	// MaxContextTokens 回傳該模型的上下文窗口大小（token）。供自適應壓縮按真實窗口設定水位線
	//（不同模型差異很大：Claude 200k、Gemini 1M、本地 Llama 可能僅 8k）。
	MaxContextTokens() int
	// ModelName 回傳模型 id（如 claude-opus-4-8），供 OTel gen_ai.request.model 與成本計算使用。
	ModelName() string
}

// Configurable 是【可選】介面：回傳一個換了模型/輸出上限的 provider 變體（原 provider 不變）。
// 供具名子 agent 選模型（model）與 effort（→輸出 token 上限）。model 空＝保留原模型；maxTokens<=0＝保留原上限。
// provider 未實作此介面時，子 agent 沿用主引擎的 provider（model/effort 靜默忽略）。
type Configurable interface {
	Configure(model string, maxTokens int) LLMProvider
}

// EffortSetter 是【可選】介面：回傳帶了思考力度（effort）的 provider 變體（原 provider 不變）。
// 供頻道的 effort 覆蓋（辦公室派工時帶的，session.Effort）。空字串＝不送、由模型自己決定。
// Claude 送 output_config.effort（型號收才送，見 ClaudeProvider.effortFor）；OpenAI 相容送 reasoning_effort。
// provider 未實作時靜默忽略——effort 是加值，不是前提。
type EffortSetter interface {
	WithEffort(effort string) LLMProvider
}
