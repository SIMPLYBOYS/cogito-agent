// internal/provider/interface.go
package provider

import (
	"context"

	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
)

// LLMProvider defines the unified interface for communicating with large models
type LLMProvider interface {
	// Generate receives the current context history and available tools list, returns the model response
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
	// MaxContextTokens 返回該模型的上下文窗口大小（token）。供自適應壓縮按真實窗口設定水位線
	//（不同模型差異很大：Claude 200k、Gemini 1M、本地 Llama 可能僅 8k）。
	MaxContextTokens() int
}
