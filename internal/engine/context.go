package engine

import (
	"context"

	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
)

// sessionCtxKey 是 ctx 中存放当前会话的私有键。
type sessionCtxKeyType struct{}

var sessionCtxKey sessionCtxKeyType

// WithSession 把当前会话放入 ctx，使下游（如 tools middleware）能取到触发该工具调用的会话。
// ch16 用它让审批 middleware 拿到 session.ID（Slack 下即 channelID），把审批请求发回对应频道。
func WithSession(ctx context.Context, s *ctxpkg.Session) context.Context {
	return context.WithValue(ctx, sessionCtxKey, s)
}

// SessionFromContext 取回 ctx 中的会话；不存在则返回 nil。
func SessionFromContext(ctx context.Context) *ctxpkg.Session {
	s, _ := ctx.Value(sessionCtxKey).(*ctxpkg.Session)
	return s
}
