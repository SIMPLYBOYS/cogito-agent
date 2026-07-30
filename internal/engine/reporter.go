package engine

import "context"

type Reporter interface {
	OnThinking(ctx context.Context)
	OnToolCall(ctx context.Context, toolName string, args string)
	OnToolResult(ctx context.Context, toolName string, result string, isError bool)
	OnMessage(ctx context.Context, content string)
	// OnTurn 在每進入一個執行回合時呼叫，供跑分量測「駕馭順滑度」（完成任務用了幾輪）。
	OnTurn(ctx context.Context, turn int)
}

// MultiReporter 把事件同時發給多個 Reporter（如終端 + 辦公室投影）。
type MultiReporter []Reporter

func (m MultiReporter) OnThinking(ctx context.Context) {
	for _, r := range m {
		r.OnThinking(ctx)
	}
}
func (m MultiReporter) OnTurn(ctx context.Context, t int) {
	for _, r := range m {
		r.OnTurn(ctx, t)
	}
}
func (m MultiReporter) OnToolCall(ctx context.Context, n, a string) {
	for _, r := range m {
		r.OnToolCall(ctx, n, a)
	}
}
func (m MultiReporter) OnToolResult(ctx context.Context, n, res string, e bool) {
	for _, r := range m {
		r.OnToolResult(ctx, n, res, e)
	}
}
func (m MultiReporter) OnMessage(ctx context.Context, c string) {
	for _, r := range m {
		r.OnMessage(ctx, c)
	}
}

var _ Reporter = (MultiReporter)(nil)
