package chatbot

import (
	"context"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

// 入口平權：SetHooks 必須把整包都掛上。漏掉任一欄位＝某個入口的自我進化靜默失效
// （office HTTP 入口就這樣漏過 postRun/postFailure），而那種漏法不會有任何錯誤訊息。
func TestSetHooks_AssignsAll(t *testing.T) {
	c := NewCore("hookstest", t.TempDir(), nil, func(string, string) {})

	var ranPost, ranFail, ranLearn bool
	c.SetHooks(Hooks{
		PostRun:     func(context.Context, *ctxpkg.Session, string) { ranPost = true },
		PostFailure: func(context.Context, *ctxpkg.Session, string, string) { ranFail = true },
		Learn:       func(context.Context, *ctxpkg.Session) (string, error) { ranLearn = true; return "", nil },
	})

	if c.postRun == nil || c.postFailure == nil || c.learn == nil {
		t.Fatalf("三個鉤子都該被掛上：postRun=%v postFailure=%v learn=%v",
			c.postRun != nil, c.postFailure != nil, c.learn != nil)
	}
	// 掛上的確實是傳進去的那個（防「都非 nil 但接錯欄位」）
	c.postRun(context.Background(), nil, "")
	c.postFailure(context.Background(), nil, "", "")
	_, _ = c.learn(context.Background(), nil)
	if !ranPost || !ranFail || !ranLearn {
		t.Errorf("鉤子接錯欄位：post=%v fail=%v learn=%v", ranPost, ranFail, ranLearn)
	}

	// 零值 Hooks＝全部停用（等同過去傳 nil），不該留著上一組
	c.SetHooks(Hooks{})
	if c.postRun != nil || c.postFailure != nil || c.learn != nil {
		t.Error("零值 Hooks 應清空全部鉤子")
	}
}
