// internal/tools/registry.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/SIMPLYBOYS/go-tiny-claw/internal/observability"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
)

type BaseTool interface {
	Name() string
	Definition() schema.ToolDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolHandler 是中間件鏈中的「下一步」，最終落到工具本身的執行。
type ToolHandler func(ctx context.Context, call schema.ToolCall) schema.ToolResult

// MiddlewareFunc 是環繞式（around）工具中間件：拿到 call 與 next（鏈的下一步），可在 next() 前後
// 插入邏輯（計時/日誌/重試/快取），也可不調用 next() 直接短路返回（如審批拒絕）。tools 包只
// 暴露這個 hook 點，完全不感知具體業務（如 IM 平臺審批）。
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult

type Registry interface {
	Register(tool BaseTool)
	Use(mw MiddlewareFunc) // 全局 middleware 掛載點
	GetAvailableTools() []schema.ToolDefinition
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

type registryImpl struct {
	tools       map[string]BaseTool
	middlewares []MiddlewareFunc // 中間件鏈，Execute 前依次執行
}

func NewRegistry() Registry {
	return &registryImpl{
		tools:       make(map[string]BaseTool),
		middlewares: make([]MiddlewareFunc, 0),
	}
}

func (r *registryImpl) Use(mw MiddlewareFunc) {
	r.middlewares = append(r.middlewares, mw)
}

func (r *registryImpl) Register(tool BaseTool) {
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		log.Printf("[Warning] 工具 '%s' 已經被註冊，將被覆蓋。\n", name)
	}
	r.tools[name] = tool
	log.Printf("[Registry] 成功掛載工具: %s\n", name)
}

func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	var defs []schema.ToolDefinition
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	return defs
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	// 【埋點 5】開啟工具執行 Span（無論成敗，defer 確保結束）
	ctx, span := observability.StartSpan(ctx, "Tool.Execute")
	span.AddAttribute("tool_name", call.Name)
	span.AddAttribute("arguments", string(call.Arguments))
	defer span.EndSpan()

	tool, exists := r.tools[call.Name]
	if !exists {
		return schema.ToolResult{
			ToolCallID: call.ID,
			Output:     fmt.Sprintf("Error: 系統中不存在名為 '%s' 的工具。", call.Name),
			IsError:    true,
		}
	}

	// 最內層 handler：真正執行工具底層邏輯。
	handler := func(ctx context.Context, call schema.ToolCall) schema.ToolResult {
		output, err := tool.Execute(ctx, call.Arguments)
		if err != nil {
			return schema.ToolResult{
				ToolCallID: call.ID,
				Output:     fmt.Sprintf("Error executing %s: %v", call.Name, err),
				IsError:    true,
			}
		}
		// 只截前 100 字符放進 Trace，防止 trace 文件膨脹
		span.AddAttribute("output_preview", truncate(output, 100))
		return schema.ToolResult{
			ToolCallID: call.ID,
			Output:     output,
			IsError:    false,
		}
	}

	// 由內而外包裝中間件：註冊順序靠前者位於外層（最先進入、最後返回）。
	// 環繞式 middleware 可在 next() 前後計時/日誌，或不調 next() 直接短路（如審批拒絕）。
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		mw := r.middlewares[i]
		next := handler
		handler = func(ctx context.Context, call schema.ToolCall) schema.ToolResult {
			return mw(ctx, call, next)
		}
	}

	return handler(ctx, call)
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
