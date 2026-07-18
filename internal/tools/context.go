package tools

import "context"

// callID 在 ctx 裡傳「當前正在執行的 ToolCall.ID」。registry.Execute 執行每個工具前注入；讓工具
// （及其內部再拉起的 RunSub）能拿到自己的 call id——用途：把 subagent 的內部 history 落地成
// subagents/<callID>.json，供 dashboard 的 run-tree 用同一把 id 把子 agent 內部掛回主節點。
type callIDKey struct{}

// WithCallID 把 call id 放進 ctx（registry 每次執行工具前呼叫）。
func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

// CallIDFromContext 取回當前工具的 call id；不存在（如背景子 agent 的 detached ctx）回空字串。
func CallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}
