package engine

import "context"

type Reporter interface {
	OnThinking(ctx context.Context)
	OnToolCall(ctx context.Context, toolName string, args string)
	OnToolResult(ctx context.Context, toolName string, result string, isError bool)
	OnMessage(ctx context.Context, content string)
	// OnTurn 在每進入一個執行回合時調用，供跑分量測「駕馭順滑度」（完成任務用了幾輪）。
	OnTurn(ctx context.Context, turn int)
}
