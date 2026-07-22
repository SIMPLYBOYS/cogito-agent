package chatbot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/authz"
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
		userLink:     parseUserLink(os.Getenv("COGITO_USER_LINK")), // 與 NewCore 一致，免得 helper 漂移
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

// 接線驗收：Core 的授權判定確實會走記錄檔，且【免重啟】——同一個 Core 實例，
// 批准後立刻有權、撤銷後立刻失效。這是 authz 套件單測涵蓋不到的部分（那邊測的是 store 本身）。
func TestCore_AuthzFileTakesEffectWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	envAllowed := map[string]bool{"tgwire:boss": true}
	store := authz.New(filepath.Join(dir, ".claw"), envAllowed, nil)

	c, _ := newCaptureCore(t, "tgwire", []string{"tgwire:boss"}, nil)
	c.authz = store

	if c.isAllowed("newbie") {
		t.Fatal("前置條件：newbie 尚未被授權")
	}
	if err := store.Approve("tgwire:newbie", authz.RoleUser, "tgwire:boss"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !c.isAllowed("newbie") {
		t.Error("批准後應【免重啟】立刻生效")
	}
	if c.isAdmin("newbie") {
		t.Error("RoleUser 不該取得審批權——職責分離")
	}

	if err := store.Revoke("tgwire:newbie", "tgwire:boss"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if c.isAllowed("newbie") {
		t.Error("撤銷後應立刻失效——撤銷若要等重啟，等於沒有撤銷")
	}
}

// env bootstrap 不因記錄檔存在而失效：否則改壞檔就把所有人鎖在門外。
func TestCore_EnvBootstrapSurvivesFileAuthz(t *testing.T) {
	dir := t.TempDir()
	envAllowed := map[string]bool{"tgboot:boss": true}
	store := authz.New(filepath.Join(dir, ".claw"), envAllowed, nil)
	_ = store.Approve("tgboot:other", authz.RoleUser, "tgboot:boss")

	c, _ := newCaptureCore(t, "tgboot", []string{"tgboot:boss"}, nil)
	c.authz = store

	if !c.isAllowed("boss") {
		t.Error("env bootstrap 應始終有效")
	}
	if !c.isAdmin("boss") {
		t.Error("未單獨設 admin 時 boss 應可審批（沿用既有語意）")
	}
}

// /stop 之後、任務真正收尾之前，若使用者再發訊息，必須讀得出「正在停」而不是「還在跑」——
// 否則畫面上看起來像 /stop 沒作用、系統當掉了（2026-07-22 排練實際回報）。
// ctx 取消只在【回合邊界】被檢查，這段空窗期可長達數十秒，不是邊角案例。
func TestStop_BusyMessageDistinguishesStopping(t *testing.T) {
	c, got := newCaptureCore(t, "stopmsg", []string{"boss"}, nil)
	wd := c.channelWorkDir(c.convID("chan"))

	// 模擬一個執行中的任務佔住鎖
	_, cancel := context.WithCancel(context.Background())
	if !c.tryAcquire(wd, cancel) {
		t.Fatal("前置條件：應能取得鎖")
	}
	t.Cleanup(func() { c.release(wd) })

	// ① 尚未 /stop：忙碌訊息是「仍在進行」
	c.dispatch("chan", "boss", "再做一件事", false)
	if m := lastMsg(got); !strings.Contains(m, "仍在進行") {
		t.Errorf("未請求中止時應回「仍在進行」，得到: %q", m)
	}

	// ② /stop 之後：同樣是忙碌，但訊息必須改口
	if !c.stop(wd) {
		t.Fatal("stop 應回報有任務被取消")
	}
	if !c.isStopping(wd) {
		t.Error("stop 後應標記為中止中")
	}
	c.dispatch("chan", "boss", "再做一件事", false)
	m := lastMsg(got)
	if !strings.Contains(m, "中止進行中") {
		t.Errorf("已請求中止時應明說正在停，得到: %q", m)
	}
	if strings.Contains(m, "仍在進行") {
		t.Errorf("不該再說「仍在進行」——那正是讓人以為當機的措辭: %q", m)
	}

	// ③ 收尾後旗標要清掉，否則下一個任務會一直被誤報成「中止中」
	c.release(wd)
	if c.isStopping(wd) {
		t.Error("release 後應清掉中止旗標")
	}
}
