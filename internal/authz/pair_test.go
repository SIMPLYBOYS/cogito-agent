package authz

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 完整流程：未授權者請求 → 管理員以碼批准 → 立刻有權，且待審佇列清空。
func TestPair_RequestApproveFlow(t *testing.T) {
	s := newStore(t, map[string]bool{"tg:boss": true}, nil)

	req, err := s.RequestPair("tg", "newbie", "Aaron")
	if err != nil {
		t.Fatalf("RequestPair: %v", err)
	}
	if req.Code == "" || req.Entry != "tg:newbie" {
		t.Fatalf("請求內容不對: %+v", req)
	}
	if pending, _ := s.Pending(); len(pending) != 1 {
		t.Fatalf("應有 1 筆待審，得到 %d", len(pending))
	}

	if _, err := s.ApprovePair(req.Code, RoleUser, "tg:boss"); err != nil {
		t.Fatalf("ApprovePair: %v", err)
	}
	allowed, _, _ := s.Sets()
	if !allowed["tg:newbie"] {
		t.Error("批准後應立刻有權")
	}
	if pending, _ := s.Pending(); len(pending) != 0 {
		t.Errorf("批准後待審應清空，還剩 %d", len(pending))
	}
}

// 否決只移除待審，不得授予任何權限。
func TestPair_RejectGrantsNothing(t *testing.T) {
	s := newStore(t, map[string]bool{"tg:boss": true}, nil)
	req, _ := s.RequestPair("tg", "newbie", "")

	if _, err := s.RejectPair(req.Code); err != nil {
		t.Fatalf("RejectPair: %v", err)
	}
	if allowed, _, _ := s.Sets(); allowed["tg:newbie"] {
		t.Error("否決後不該有任何權限")
	}
	if pending, _ := s.Pending(); len(pending) != 0 {
		t.Error("否決後待審應清空")
	}
}

// 碼是短期憑證：過期就不能用，且不會出現在待審清單。
func TestPair_ExpiredCodeUnusable(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, map[string]bool{"tg:boss": true}, nil)
	req, _ := s.RequestPair("tg", "newbie", "")

	// 直接把過期時間改到過去（比等 10 分鐘實際）。
	path := filepath.Join(dir, PendingFileName)
	data, _ := os.ReadFile(path)
	var doc struct {
		Pending []PairRequest `json:"pending"`
	}
	_ = json.Unmarshal(data, &doc)
	doc.Pending[0].ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339)
	out, _ := json.Marshal(doc)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}

	if pending, _ := s.Pending(); len(pending) != 0 {
		t.Error("過期項不該出現在待審清單")
	}
	if _, err := s.ApprovePair(req.Code, RoleUser, "tg:boss"); err == nil {
		t.Error("過期碼不該還能批准")
	}
	if allowed, _, _ := s.Sets(); allowed["tg:newbie"] {
		t.Error("過期碼絕不可授予權限")
	}
}

// 壞掉的時間格式一律視為過期（fail-closed）——別讓髒資料變成永久有效的碼。
func TestPairRequest_BadTimestampCountsAsExpired(t *testing.T) {
	if !(PairRequest{ExpiresAt: "not-a-time"}).Expired(time.Now()) {
		t.Error("時間格式壞掉應視為過期")
	}
}

// 同一人重複請求＝換發新碼，不累積佇列（否則一個人就能塞爆待審）。
func TestPair_RerequestReplacesInsteadOfStacking(t *testing.T) {
	s := newStore(t, map[string]bool{"tg:boss": true}, nil)
	first, _ := s.RequestPair("tg", "newbie", "")
	second, err := s.RequestPair("tg", "newbie", "")
	if err != nil {
		t.Fatalf("重複請求: %v", err)
	}
	if pending, _ := s.Pending(); len(pending) != 1 {
		t.Errorf("同一人應只有 1 筆待審，得到 %d", len(pending))
	}
	if _, err := s.ApprovePair(first.Code, RoleUser, "tg:boss"); err == nil {
		t.Error("舊碼應已失效")
	}
	if _, err := s.ApprovePair(second.Code, RoleUser, "tg:boss"); err != nil {
		t.Errorf("新碼應可用: %v", err)
	}
}

// 已有權的人不必配對——免得管理員收到無意義的待審。
func TestPair_AlreadyAuthorizedRejected(t *testing.T) {
	s := newStore(t, map[string]bool{"tg:boss": true}, nil)
	if _, err := s.RequestPair("tg", "boss", ""); err == nil {
		t.Error("已授權者不該能發起配對")
	}
}

// 待審佇列有上限：任何人都能發起請求，沒有上限就是免費的洗版管道。
// 額滿時拒絕【新】請求，而非淘汰舊的——否則攻擊者可以把真人的請求擠掉。
func TestPair_QueueCapRejectsNewNotOld(t *testing.T) {
	s := newStore(t, map[string]bool{"tg:boss": true}, nil)
	first, _ := s.RequestPair("tg", "user0", "")
	for i := 1; i < maxPending; i++ {
		if _, err := s.RequestPair("tg", "user"+string(rune('a'+i)), ""); err != nil {
			t.Fatalf("第 %d 筆不該失敗: %v", i, err)
		}
	}
	if _, err := s.RequestPair("tg", "overflow", ""); err == nil {
		t.Error("超過上限應被拒")
	}
	// 最早那筆仍在，沒被擠掉。
	if _, err := s.ApprovePair(first.Code, RoleUser, "tg:boss"); err != nil {
		t.Errorf("額滿不該淘汰既有請求: %v", err)
	}
}

// 碼要能用小寫打——人會照唸照打，不會管大小寫。
func TestPair_CodeMatchIsCaseInsensitive(t *testing.T) {
	s := newStore(t, map[string]bool{"tg:boss": true}, nil)
	req, _ := s.RequestPair("tg", "newbie", "")
	if _, err := s.ApprovePair(strings.ToLower(req.Code), RoleUser, "tg:boss"); err != nil {
		t.Errorf("小寫碼應可用: %v", err)
	}
}

// 碼不可預測（crypto/rand）且避開易混淆字元。
func TestNewPairCode_UnpredictableAndUnambiguous(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := newPairCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 6 {
			t.Fatalf("碼長應為 6，得到 %q", c)
		}
		if strings.ContainsAny(c, "01OIL") {
			t.Errorf("碼不應含易混淆字元: %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 190 {
		t.Errorf("碼重複率過高（200 次只有 %d 個相異），可能不是真隨機", len(seen))
	}
}
