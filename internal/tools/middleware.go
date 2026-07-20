package tools

import (
	"context"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// NewTimingMiddleware 回傳一個環繞式中間件，量測【工具本身】的物理執行耗時（毫秒）並交給
// record 回調（record 為 nil 時不記錄）。它不感知任何具體工具，靠 Registry.Use 掛載即可。
//
// 用法提示：把它註冊在會阻塞等待的中間件（如人工審批）【之後】，這樣計時只涵蓋工具真正執行的
// 時間，不把人工審批的等待算進去（註冊順序靠前者為外層）。
func NewTimingMiddleware(record func(toolName string, durationMs int64)) MiddlewareFunc {
	return func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
		start := time.Now()
		res := next(ctx, call)
		if record != nil {
			record(call.Name, time.Since(start).Milliseconds())
		}
		return res
	}
}
