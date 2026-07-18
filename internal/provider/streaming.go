package provider

import (
	"context"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// StreamingProvider 是 LLMProvider 的【可選】擴充：支援逐 token 串流生成。engine 只在（1）context 帶了
// delta sink 且（2）provider 實作本介面時才走串流；否則退回 Generate。因此不實作本介面的 provider
// （及所有不設 sink 的呼叫端：bot / cli / bench）行為完全不變。
//
// onDelta 收到「文字」增量（純文字 token，不含 tool_use 的 JSON 增量）——供 UI 逐字顯示。回傳仍是
// 完整組裝好的 Message（含 tool calls 與 Usage），故引擎主迴圈邏輯與非串流完全一致。
type StreamingProvider interface {
	GenerateStream(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition, onDelta func(string)) (*schema.Message, error)
}
