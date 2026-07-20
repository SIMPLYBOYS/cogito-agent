// internal/tools/registry.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"

	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

type BaseTool interface {
	Name() string
	Definition() schema.ToolDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolHandler 是中間件鏈中的「下一步」，最終落到工具本身的執行。
type ToolHandler func(ctx context.Context, call schema.ToolCall) schema.ToolResult

// MiddlewareFunc 是環繞式（around）工具中間件：拿到 call 與 next（鏈的下一步），可在 next() 前後
// 插入邏輯（計時/日誌/重試/快取），也可不呼叫 next() 直接短路回傳（如審批拒絕）。tools 包只
// 暴露這個 hook 點，完全不感知具體業務（如 IM 平臺審批）。
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult

type Registry interface {
	Register(tool BaseTool)
	Use(mw MiddlewareFunc) // 全域 middleware 掛載點
	GetAvailableTools() []schema.ToolDefinition
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
	Subset(names []string) Registry // 取只含指定工具的子註冊表（沿用同一組 middleware），供具名子 agent 限縮能力
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

// Subset 回傳只含 names 內工具的新註冊表，沿用同一組 middleware（審批/計時仍生效）。
// 不存在的名稱略過。供具名子 agent 依其 tools 宣告限縮能力（只能是既有工具的子集）。
func (r *registryImpl) Subset(names []string) Registry {
	sub := &registryImpl{tools: make(map[string]BaseTool), middlewares: r.middlewares}
	for _, n := range names {
		if t, ok := r.tools[n]; ok {
			sub.tools[n] = t
		}
	}
	return sub
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
	defs := make([]schema.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	// 按名稱排序：r.tools 是 map，迭代順序隨機；若不排序，每輪送給 LLM 的 tools 陣列順序都不同，
	// prompt cache 的前綴（tools+system）就無法位元組一致 → 幾乎每輪 miss、且 cache_control 標到
	// 的「最後工具」每輪都不同。固定順序後快取才能穩定命中。
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	// 【埋點 5】開啟工具執行 Span（無論成敗，defer 確保結束）
	ctx, span := observability.StartSpan(ctx, "Tool.Execute")
	ctx = WithCallID(ctx, call.ID) // 讓工具（及其 RunSub）拿得到自己的 call id（見 context.go）
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

	// 最內層 handler：真正執行工具底層邏輯。executed 標記工具是否被觸達——若中間件短路
	// （如審批拒絕，未呼叫 next），它會維持 false，據此在 span 標記攔截。
	executed := false
	handler := func(ctx context.Context, call schema.ToolCall) schema.ToolResult {
		executed = true
		output, err := tool.Execute(ctx, call.Arguments)
		if err != nil {
			return schema.ToolResult{
				ToolCallID: call.ID,
				Output:     fmt.Sprintf("Error executing %s: %v", call.Name, err),
				IsError:    true,
			}
		}
		// 截前 N 字元放進 Trace 預覽，防止 trace 膨脹（過小會讓 Langfuse 看到一堆 ...）
		span.AddAttribute("output_preview", truncate(output, maxPreviewChars))
		return schema.ToolResult{
			ToolCallID: call.ID,
			Output:     output,
			IsError:    false,
		}
	}

	// 由內而外包裝中間件：註冊順序靠前者位於外層（最先進入、最後回傳）。
	// 環繞式 middleware 可在 next() 前後計時/日誌，或不調 next() 直接短路（如審批拒絕）。
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		mw := r.middlewares[i]
		next := handler
		handler = func(ctx context.Context, call schema.ToolCall) schema.ToolResult {
			return mw(ctx, call, next)
		}
	}

	result := handler(ctx, call)

	// 中間件短路（未觸達工具）→ 在 span 標記攔截，便於 trace 回放分辨「被攔」與「工具自身報錯」。
	if !executed {
		log.Printf("[Registry] ⚠️ 工具 %s 被 middleware 短路攔截\n", call.Name)
		span.AddAttribute("intercepted", true)
		span.AddAttribute("reject_reason", truncate(result.Output, maxPreviewChars))
	}

	return result
}

// maxPreviewChars 是放進 trace 預覽屬性的字元上限（rune，非 byte）。100 太小會讓 Langfuse
// 滿屏 ...；放大到 2000 仍遠小於工具原始輸出，避免 span 膨脹。
const maxPreviewChars = 2000

// truncate 按 rune 截斷（非 byte），避免切到中文多位元組字元中間。
func truncate(s string, max int) string {
	return schema.TruncRunes(s, max, "...")
}
