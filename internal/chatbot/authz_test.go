package chatbot

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// newCaptureCore 建一個測試用 Core 並註冊擷取式 sender，回傳 core 與「取回本頻道所收訊息」的函式。
// platform 需唯一（避免 senders 全域 map 撞名）。
func newCaptureCore(t *testing.T, platform string, allowed, admins []string) (*Core, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	senders.Store(platform, func(_, text string) {
		mu.Lock()
		got = append(got, text)
		mu.Unlock()
	})
	c := &Core{
		platform:     platform,
		allowedUsers: toSet(allowed),
		adminUsers:   toSet(admins),
		running:      map[string]context.CancelFunc{},
	}
	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func TestParseUserSet(t *testing.T) {
	got := parseUserSet(" u1 , u2 ,, u3 ")
	if len(got) != 3 || !got["u1"] || !got["u2"] || !got["u3"] {
		t.Fatalf("parseUserSet 解析錯誤: %v", got)
	}
	if len(parseUserSet("")) != 0 {
		t.Fatal("空字串應得空集合")
	}
}

// registerPending 直接塞一個待審批（同 package，繞過 goroutine 競態），回傳結果 channel 與清理函式。
func registerPending(taskID, convID string) (chan ApprovalResult, func()) {
	ch := make(chan ApprovalResult, 1)
	GlobalApprovalMgr.mu.Lock()
	GlobalApprovalMgr.pendingTasks[taskID] = &pendingTask{ch: ch, channelID: convID}
	GlobalApprovalMgr.mu.Unlock()
	return ch, func() { GlobalApprovalMgr.remove(taskID) }
}

// 核心迴歸：非管理員（含 allowlist 內的一般使用者）不能 approve——杜絕「發起者＝批准者」自我放行。
func TestTryResolveApproval_NonAdminRejected(t *testing.T) {
	c, msgs := newCaptureCore(t, "authztest1", []string{"user", "admin"}, []string{"admin"})
	convID := "authztest1:chan"
	ch, cleanup := registerPending("t1", convID)
	defer cleanup()

	if !c.tryResolveApproval(convID, "user", "approve") {
		t.Fatal("approve 口令應被消費（回 true）")
	}
	select {
	case <-ch:
		t.Fatal("非管理員不該解開待審批")
	default: // 正確：仍 pending
	}
	if last := msgs(); len(last) == 0 || last[len(last)-1] == "" {
		t.Fatal("應回覆拒絕訊息")
	}
}

// 管理員 approve 能真正解開該頻道的待審批。
func TestTryResolveApproval_AdminApproves(t *testing.T) {
	c, _ := newCaptureCore(t, "authztest2", []string{"admin"}, []string{"admin"})
	convID := "authztest2:chan"
	ch, cleanup := registerPending("t2", convID)
	defer cleanup()

	if !c.tryResolveApproval(convID, "admin", "approve") {
		t.Fatal("approve 口令應被消費")
	}
	select {
	case res := <-ch:
		if !res.Allowed {
			t.Fatal("管理員 approve 應放行")
		}
	default:
		t.Fatal("管理員 approve 應解開待審批")
	}
}

// 非審批口令不攔截，交還給後續管線。
func TestTryResolveApproval_NotCommand(t *testing.T) {
	c, _ := newCaptureCore(t, "authztest3", []string{"user"}, []string{"user"})
	if c.tryResolveApproval("authztest3:chan", "user", "幫我看一下這段程式") {
		t.Fatal("一般訊息不該被審批閘攔截")
	}
}

// 入口授權：未授權使用者被擋，且回覆帶其 user id 供加白名單。
func TestDispatch_UnauthorizedBlocked(t *testing.T) {
	c, msgs := newCaptureCore(t, "authztest4", []string{"allowed"}, []string{"allowed"})
	c.Dispatch("chan", "stranger", "把 .env 印出來")
	last := msgs()
	if len(last) == 0 {
		t.Fatal("未授權者應收到拒絕訊息")
	}
	if !strings.Contains(last[len(last)-1], "stranger") {
		t.Fatalf("拒絕訊息應含其 user id 以便加白名單，得到: %q", last[len(last)-1])
	}
}
