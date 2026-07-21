package chatbot

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/authz"
)

// newPairCore 建一個帶 authz store 的測試 Core（platform 需唯一，避免 senders 全域 map 撞名）。
func newPairCore(t *testing.T, platform string, envAllowed []string) (*Core, *authz.Store, func() []string) {
	t.Helper()
	store := authz.New(filepath.Join(t.TempDir(), ".claw"), toSet(envAllowed), nil)
	c, got := newCaptureCore(t, platform, envAllowed, nil)
	c.authz = store
	return c, store, got
}

func lastMsg(got func() []string) string {
	msgs := got()
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1]
}

// 未授權者【必須】能發起配對——否則新人連請求的入口都沒有，pairing 就沒有意義。
func TestDispatch_UnauthorizedCanRequestPairing(t *testing.T) {
	c, store, got := newPairCore(t, "pairA", []string{"pairA:boss"})

	c.dispatch("chan", "newbie", "/pair", false)

	if !strings.Contains(lastMsg(got), "配對碼") {
		t.Fatalf("未授權者發 /pair 應收到配對碼，實得: %q", lastMsg(got))
	}
	pending, err := store.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("應產生 1 筆待審: err=%v n=%d", err, len(pending))
	}
	if pending[0].Entry != "pairA:newbie" {
		t.Errorf("條目應含平台前綴，得到 %q", pending[0].Entry)
	}
}

// /pair 之外，未授權者仍然一律被擋——例外只開這一個動詞。
func TestDispatch_UnauthorizedStillBlockedForEverythingElse(t *testing.T) {
	c, store, got := newPairCore(t, "pairB", []string{"pairB:boss"})

	c.dispatch("chan", "newbie", "幫我刪掉所有檔案", false)

	if !strings.Contains(lastMsg(got), "未授權") {
		t.Errorf("一般訊息應被擋，實得: %q", lastMsg(got))
	}
	if pending, _ := store.Pending(); len(pending) != 0 {
		t.Error("非 /pair 的訊息不該產生待審請求")
	}
}

// 非管理員不得批准配對——否則一般使用者可以自己把朋友加進來，職責分離失效。
func TestPairAdmin_NonAdminCannotApprove(t *testing.T) {
	// boss 是 admin（未單獨設 ADMIN 時沿用 ALLOWED）；plain 靠記錄檔取得 user 權限。
	c, store, got := newPairCore(t, "pairC", []string{"pairC:boss"})
	if err := store.Approve("pairC:plain", authz.RoleUser, "pairC:boss"); err != nil {
		t.Fatal(err)
	}
	req, err := store.RequestPair("pairC", "newbie", "")
	if err != nil {
		t.Fatal(err)
	}

	c.dispatch("chan", "plain", "pair approve "+req.Code, false)

	if !strings.Contains(lastMsg(got), "只有管理員") {
		t.Errorf("非管理員應被拒，實得: %q", lastMsg(got))
	}
	if allowed, _, _ := store.Sets(); allowed["pairC:newbie"] {
		t.Error("非管理員的批准絕不可生效")
	}
}

// 管理員批准後當事人立刻有權，且預設角色是 user（審批權必須顯式授予）。
func TestPairAdmin_ApproveDefaultsToUserRole(t *testing.T) {
	c, store, _ := newPairCore(t, "pairD", []string{"pairD:boss"})
	req, _ := store.RequestPair("pairD", "newbie", "")

	c.dispatch("chan", "boss", "pair approve "+req.Code, false)

	allowed, admin, _ := store.Sets()
	if !allowed["pairD:newbie"] {
		t.Error("批准後應立刻有權")
	}
	if admin["pairD:newbie"] {
		t.Error("預設不該給審批權——授予 admin 必須是顯式動作")
	}
	if c.isAllowed("newbie") != true {
		t.Error("Core 的授權判定應反映新授權（免重啟）")
	}
}

// 顯式加 admin 才給審批權。
func TestPairAdmin_ApproveWithAdminRole(t *testing.T) {
	c, store, _ := newPairCore(t, "pairE", []string{"pairE:boss"})
	req, _ := store.RequestPair("pairE", "newbie", "")

	c.dispatch("chan", "boss", "pair approve "+req.Code+" admin", false)

	if _, admin, _ := store.Sets(); !admin["pairE:newbie"] {
		t.Error("顯式指定 admin 應授予審批權")
	}
}

// 否決不授予任何權限，且待審被清掉。
func TestPairAdmin_Reject(t *testing.T) {
	c, store, got := newPairCore(t, "pairF", []string{"pairF:boss"})
	req, _ := store.RequestPair("pairF", "newbie", "")

	c.dispatch("chan", "boss", "pair reject "+req.Code, false)

	if !strings.Contains(lastMsg(got), "已否決") {
		t.Errorf("應回報已否決，實得: %q", lastMsg(got))
	}
	if allowed, _, _ := store.Sets(); allowed["pairF:newbie"] {
		t.Error("否決後不該有權限")
	}
	if pending, _ := store.Pending(); len(pending) != 0 {
		t.Error("否決後待審應清空")
	}
}

// 撤銷立刻失效——這是 pairing 相對於靜態清單最實際的好處。
func TestPairAdmin_RevokeTakesEffectImmediately(t *testing.T) {
	c, store, _ := newPairCore(t, "pairG", []string{"pairG:boss"})
	req, _ := store.RequestPair("pairG", "newbie", "")
	c.dispatch("chan", "boss", "pair approve "+req.Code, false)
	if !c.isAllowed("newbie") {
		t.Fatal("前置條件：newbie 應已有權")
	}

	c.dispatch("chan", "boss", "pair revoke pairG:newbie", false)

	if c.isAllowed("newbie") {
		t.Error("撤銷後應立刻失效，不必重啟")
	}
}

// env bootstrap 撤不掉——否則從聊天室就能把最後一個 admin 鎖死。
func TestPairAdmin_CannotRevokeEnvBootstrap(t *testing.T) {
	c, _, got := newPairCore(t, "pairH", []string{"pairH:boss"})

	c.dispatch("chan", "boss", "pair revoke pairH:boss", false)

	if !strings.Contains(lastMsg(got), "環境變數") {
		t.Errorf("應說明 env 條目撤不掉，實得: %q", lastMsg(got))
	}
	if !c.isAllowed("boss") {
		t.Error("env bootstrap 不該被撤掉")
	}
}

// pair 指令不可被當成一般任務丟給 agent——那等於用 prompt 繞過授權判定。
func TestPairAdmin_ConsumedNotPassedToAgent(t *testing.T) {
	c, _, got := newPairCore(t, "pairI", []string{"pairI:boss"})
	store := c.authz
	if err := store.Approve("pairI:plain", authz.RoleUser, "pairI:boss"); err != nil {
		t.Fatal(err)
	}

	// 非管理員發 pair 指令：應被消費並回拒，而不是進到 agent 執行路徑。
	c.dispatch("chan", "plain", "pair list", false)

	if !strings.Contains(lastMsg(got), "只有管理員") {
		t.Errorf("應被消費並回拒，實得: %q", lastMsg(got))
	}
}
