package tools

import "context"

// TaskContext 是「這次工具呼叫是誰、為了哪個任務」——支付授權的 task binding 要靠它。
//
// 【為何放 tools 而不是 engine】engine 匆匆 import tools（registry），tools 反過來 import engine 會成環。
// 而 task binding 是工具（request_payment）要讀的東西，所以鍵放在被依賴的那一端。
// handleAgentRun 在起任務時設一次，順著 ctx 流進每一次 Execute。
//
// 【為何 TaskID 是新東西】cogito 原本只有 session（per-channel，跨任務累加）與 tool call id
// （per-呼叫）。支付要綁的是「這一次派工」——比 session 細、比一次呼叫粗。錢包層之所以做不到
// task binding，就是因為只有 harness 知道這個粒度存在。
type TaskContext struct {
	AgentID string // 頻道／員工身分，如 office:p19
	TaskID  string // 這一次派工的 id
}

type taskCtxKey struct{}

// WithTask 把任務身分放進 ctx。
func WithTask(ctx context.Context, t TaskContext) context.Context {
	return context.WithValue(ctx, taskCtxKey{}, t)
}

// TaskFromContext 取回任務身分；沒設就是零值（TaskID 空＝無主，policy 會 Deny）。
func TaskFromContext(ctx context.Context) TaskContext {
	t, _ := ctx.Value(taskCtxKey{}).(TaskContext)
	return t
}
