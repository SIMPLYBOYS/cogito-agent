package engine

import (
	"context"

	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// StreamSink 收 LLM 生成的「文字」token 增量，供 UI 逐字顯示。經 context 傳遞：只有設了 sink 的呼叫端
// （如 dashboard operator chat）才會啟用串流；bot/cli/bench 不設 → 完全走原本的一次性 Generate。
type StreamSink func(delta string)

type streamSinkKey struct{}

// WithStreamSink 把 delta sink 綁進 context。
func WithStreamSink(ctx context.Context, sink StreamSink) context.Context {
	return context.WithValue(ctx, streamSinkKey{}, sink)
}

// StreamSinkFromContext 取出 delta sink（無則 nil）。
func StreamSinkFromContext(ctx context.Context) StreamSink {
	if s, ok := ctx.Value(streamSinkKey{}).(StreamSink); ok {
		return s
	}
	return nil
}

// generateAction 是主迴圈的生成呼叫：ctx 帶了 delta sink 且 provider 支援串流時走逐 token 串流（把文字
// 增量餵給 sink），否則走一次性 Generate。無論哪條路，回傳的都是完整 Message，主迴圈後續邏輯一致。
// 只在主迴圈用（子 agent RunSub 不接 sink，故其文字不進 operator chat 的逐字串流）。
func (e *AgentEngine) generateAction(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	if sink := StreamSinkFromContext(ctx); sink != nil {
		if sp, ok := e.provider.(provider.StreamingProvider); ok {
			return sp.GenerateStream(ctx, msgs, tools, func(d string) { sink(d) })
		}
	}
	return e.provider.Generate(ctx, msgs, tools)
}
