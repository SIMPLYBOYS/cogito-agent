// internal/tools/registry.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/yourname/go-tiny-claw/internal/observability"
	"github.com/yourname/go-tiny-claw/internal/schema"
)

type BaseTool interface {
	Name() string
	Definition() schema.ToolDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// MiddlewareFunc 定義了工具中間件的簽名：接收當前 ToolCall，返回是否允許執行
// 以及被攔截時的原因。tools 包只暴露這個 hook 點，完全不感知具體業務（如 IM 平臺審批）。
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall) (allowed bool, rejectReason string)

type Registry interface {
	Register(tool BaseTool)
	Use(mw MiddlewareFunc) // ch16: 全局 middleware 掛載點
	GetAvailableTools() []schema.ToolDefinition
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

type registryImpl struct {
	tools       map[string]BaseTool
	middlewares []MiddlewareFunc // ch16: 中間件鏈，Execute 前依次執行
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
	// ch19【埋點 5】開啟工具執行 Span（無論成敗，defer 確保結束）
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

	// ch16【核心防禦】執行底層邏輯前，依次運行所有 middleware；任一拒絕則短路。
	for _, mw := range r.middlewares {
		allowed, reason := mw(ctx, call)
		if !allowed {
			log.Printf("[Registry] ⚠️ 工具 %s 被 middleware 攔截: %s\n", call.Name, reason)
			span.AddAttribute("intercepted", true)
			span.AddAttribute("reject_reason", reason)
			return schema.ToolResult{
				ToolCallID: call.ID,
				Output:     fmt.Sprintf("執行被系統攔截。原因: %s", reason),
				IsError:    true, // 必須返回 Error，強制大模型閱讀拒絕理由
			}
		}
	}

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

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
