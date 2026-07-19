package main

import (
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// cron 執行必須落地成 session，/runs 才看得到執行樹。這裡不呼叫 LLM，只釘住持久化路徑：
// GetOrCreate（綁 store）→ Reset → Append 之後，store 必須列得到、載得回。
func TestCronRun_PersistsSession(t *testing.T) {
	store, err := ctxpkg.NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctxpkg.GlobalSessionMgr.SetStore(store)
	defer ctxpkg.GlobalSessionMgr.SetStore(nil) // 全域單例：測完還原，別污染其他測試

	id := cronSessionID("abc123")
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(id, t.TempDir())
	sess.Reset()
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "檢查未推送的 commit"})
	sess.Append(schema.Message{Role: schema.RoleAssistant, Content: "有 3 個未推送的 commit。"})

	ids, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, got := range ids {
		if got == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("cron session %q 未出現在 store.List()（=/runs 看不到），實得 %v", id, ids)
	}

	snap, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("載入 %q 失敗（ok=%v, err=%v）", id, ok, err)
	}
	if len(snap.History) != 2 {
		t.Errorf("預期落地 2 則訊息，得 %d", len(snap.History))
	}

	if got := sess.LastAssistantText(); !strings.Contains(got, "3 個未推送") {
		t.Errorf("LastAssistantText 應取回最後的 assistant 回覆，得 %q", got)
	}
}
