package engine

import (
	"context"

	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
)

// sessionCtxKey 是 ctx 中存放當前會話的私有鍵。
type sessionCtxKeyType struct{}

var sessionCtxKey sessionCtxKeyType

// WithSession 把當前會話放入 ctx，使下游（如 tools middleware）能取到觸發該工具調用的會話。
// 用它讓審批 middleware 拿到 session.ID（Slack 下即 channelID），把審批請求發回對應頻道。
func WithSession(ctx context.Context, s *ctxpkg.Session) context.Context {
	return context.WithValue(ctx, sessionCtxKey, s)
}

// SessionFromContext 取回 ctx 中的會話；不存在則返回 nil。
func SessionFromContext(ctx context.Context) *ctxpkg.Session {
	s, _ := ctx.Value(sessionCtxKey).(*ctxpkg.Session)
	return s
}
