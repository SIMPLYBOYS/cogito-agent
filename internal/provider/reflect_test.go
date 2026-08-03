package provider

import (
	"context"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

type cfgProvider struct{ model string }

func (c *cfgProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return nil, nil
}
func (c *cfgProvider) MaxContextTokens() int { return 1000 }
func (c *cfgProvider) ModelName() string     { return c.model }
func (c *cfgProvider) Configure(model string, _ int) LLMProvider {
	return &cfgProvider{model: model}
}

// 不支援 Configurable 的 provider（如某些相容端點）
type plainProvider struct{}

func (plainProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return nil, nil
}
func (plainProvider) MaxContextTokens() int { return 1000 }
func (plainProvider) ModelName() string     { return "plain" }

func TestReflectProvider(t *testing.T) {
	main := &cfgProvider{model: "opus"}

	// 未設 → 原樣沿用（行為不變）
	t.Setenv("COGITO_REFLECT_MODEL", "")
	if got := ReflectProvider(main); got.ModelName() != "opus" {
		t.Errorf("未設應沿用主 provider，got %q", got.ModelName())
	}

	// 設了 → 換成便宜模型的變體，且【不動】主 provider
	t.Setenv("COGITO_REFLECT_MODEL", "haiku")
	got := ReflectProvider(main)
	if got.ModelName() != "haiku" {
		t.Errorf("應換成 haiku，got %q", got.ModelName())
	}
	if main.ModelName() != "opus" {
		t.Errorf("主 provider 不該被改動，got %q", main.ModelName())
	}

	// provider 不支援 Configurable → 靜默沿用，不 panic
	if got := ReflectProvider(plainProvider{}); got.ModelName() != "plain" {
		t.Errorf("不支援 Configurable 應沿用，got %q", got.ModelName())
	}

	// nil 不 panic（呼叫端可能沒有 provider）
	if ReflectProvider(nil) != nil {
		t.Error("nil 應原樣回 nil")
	}
}
