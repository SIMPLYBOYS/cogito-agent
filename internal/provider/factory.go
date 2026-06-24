package provider

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// FromEnv 依 COGITO_PROVIDER 選擇 LLM provider，回傳 (provider, modelName, error)。
//   - 未設 / claude / anthropic：Claude（需 ANTHROPIC_API_KEY；模型 CLAUDE_MODEL，預設 claude-opus-4-8）。
//   - openai：OpenAI 相容端點（需 OPENAI_API_KEY；OPENAI_BASE_URL 可指向 vLLM/Ollama/OpenRouter…；
//     模型 OPENAI_MODEL，預設 gpt-4o-mini；窗口 OPENAI_MAX_CONTEXT_TOKENS，預設 128000）。
func FromEnv() (LLMProvider, string, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COGITO_PROVIDER"))) {
	case "openai", "openai-compatible", "oai":
		cfg := OpenAIConfig{
			BaseURL:          os.Getenv("OPENAI_BASE_URL"),
			APIKey:           os.Getenv("OPENAI_API_KEY"),
			Model:            envDefault("OPENAI_MODEL", "gpt-4o-mini"),
			MaxContextTokens: envInt("OPENAI_MAX_CONTEXT_TOKENS", 128000),
		}
		if cfg.APIKey == "" {
			return nil, "", fmt.Errorf("COGITO_PROVIDER=openai 但未設 OPENAI_API_KEY")
		}
		p := NewOpenAIProvider(cfg)
		return p, p.ModelName(), nil

	case "", "claude", "anthropic":
		model := envDefault("CLAUDE_MODEL", "claude-opus-4-8")
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return nil, "", fmt.Errorf("未設 ANTHROPIC_API_KEY（Claude provider）")
		}
		return NewClaudeProvider(model), model, nil

	default:
		return nil, "", fmt.Errorf("未知的 COGITO_PROVIDER=%q（支援 claude / openai）", os.Getenv("COGITO_PROVIDER"))
	}
}

func envDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
