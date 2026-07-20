package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

type capIDTool struct{ got *string }

func (capIDTool) Name() string                      { return "capid" }
func (capIDTool) Definition() schema.ToolDefinition { return schema.ToolDefinition{Name: "capid"} }
func (c capIDTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	*c.got = CallIDFromContext(ctx)
	return "ok", nil
}

// 閉合 M2 連結鏈的第一環：registry 執行工具前把 call id 注入 ctx，工具（及其 RunSub）取得到。
func TestRegistry_InjectsCallID(t *testing.T) {
	var got string
	r := NewRegistry()
	r.Register(capIDTool{got: &got})
	r.Execute(context.Background(), schema.ToolCall{ID: "call-xyz", Name: "capid", Arguments: json.RawMessage("{}")})
	if got != "call-xyz" {
		t.Errorf("工具應能從 ctx 取到自己的 call id，got %q", got)
	}
}
