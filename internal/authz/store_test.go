package authz

import (
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T, envAllowed, envAdmin map[string]bool) *Store {
	t.Helper()
	return New(t.TempDir(), envAllowed, envAdmin)
}

// env 是 bootstrap：沒有檔案時，授權完全來自環境變數（且 admin 未設時沿用 allowed）。
func TestSets_EnvBootstrap(t *testing.T) {
	s := newStore(t, map[string]bool{"telegram:111": true}, nil)
	allowed, admin, err := s.Sets()
	if err != nil {
		t.Fatalf("Sets: %v", err)
	}
	if !allowed["telegram:111"] {
		t.Error("env 的 allowed 條目應在集合內")
	}
	if !admin["telegram:111"] {
		t.Error("未單獨設 admin 時，可對話者即可審批（沿用既有語意）")
	}
}

// 批准的人立刻進集合——這就是「免重啟生效」：同一個 Store 再查一次就看得到。
func TestApprove_TakesEffectWithoutReload(t *testing.T) {
	s := newStore(t, map[string]bool{"telegram:111": true}, nil)
	if _, admin, _ := s.Sets(); admin["telegram:222"] {
		t.Fatal("前置條件：222 尚未被授權")
	}
	if err := s.Approve("telegram:222", RoleAdmin, "telegram:111"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	allowed, admin, err := s.Sets()
	if err != nil {
		t.Fatalf("Sets: %v", err)
	}
	if !admin["telegram:222"] {
		t.Error("批准為 admin 後應能審批")
	}
	if !allowed["telegram:222"] {
		t.Error("admin 應蘊含 user——能批卻不能用不是任何人要的狀態")
	}
}

// 撤銷要【立刻】失效。撤銷若要等重啟，等於沒有撤銷。
func TestRevoke_TakesEffectImmediately(t *testing.T) {
	s := newStore(t, map[string]bool{"telegram:111": true}, nil)
	if err := s.Approve("telegram:222", RoleUser, "telegram:111"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := s.Revoke("telegram:222", "telegram:111"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	allowed, _, _ := s.Sets()
	if allowed["telegram:222"] {
		t.Error("撤銷後不應再有權限")
	}
}

// 撤銷保留記錄——稽核軌跡正是這一層存在的理由，刪掉就白做了。
func TestRevoke_KeepsAuditTrail(t *testing.T) {
	s := newStore(t, map[string]bool{"a": true}, nil)
	_ = s.Approve("b", RoleUser, "a")
	_ = s.Revoke("b", "a")

	recs, err := s.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("撤銷應保留記錄，得到 %d 筆", len(recs))
	}
	r := recs[0]
	if r.Status != StatusRevoked || r.ApprovedBy != "a" || r.RevokedBy != "a" ||
		r.ApprovedAt == "" || r.RevokedAt == "" {
		t.Errorf("軌跡欄位不完整: %+v", r)
	}
}

// env 的 bootstrap 條目撤不掉：否則從 UI 就能鎖死最後一個 admin，且重啟後又活過來。
func TestRevoke_RefusesEnvBootstrap(t *testing.T) {
	s := newStore(t, map[string]bool{"telegram:111": true}, nil)
	if err := s.Revoke("telegram:111", "telegram:111"); err == nil {
		t.Error("應拒絕撤銷 env 來源的授權")
	}
}

// 壞檔不靜默：回 err 且退回 env-only。既不放行任何人，也不把 bootstrap admin 一起鎖在門外。
func TestSets_BrokenFileFallsBackToEnvAndErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(dir, map[string]bool{"telegram:111": true}, nil)

	allowed, _, err := s.Sets()
	if err == nil {
		t.Error("壞檔應回報錯誤，不可靜默")
	}
	if !allowed["telegram:111"] {
		t.Error("壞檔時仍應保留 env bootstrap，否則沒人能進去修")
	}
}

// 壞檔時不可覆寫——會把既有記錄一起弄丟。
func TestApprove_RefusesToClobberBrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(dir, nil, nil)
	if err := s.Approve("x", RoleUser, "a"); err == nil {
		t.Error("壞檔時 Approve 應失敗")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{ not json" {
		t.Error("壞檔內容不應被覆寫")
	}
}

// 重複批准是更新而非新增一筆，且角色可調整。
func TestApprove_UpdatesInsteadOfDuplicating(t *testing.T) {
	s := newStore(t, nil, nil)
	_ = s.Approve("b", RoleUser, "a")
	_ = s.Approve("b", RoleAdmin, "a")

	recs, _ := s.Records()
	if len(recs) != 1 {
		t.Fatalf("重複批准應更新既有記錄，得到 %d 筆", len(recs))
	}
	if recs[0].Role != RoleAdmin {
		t.Errorf("角色應更新為 admin，得到 %q", recs[0].Role)
	}
}

// 重新批准要清掉舊的撤銷痕跡，否則記錄自相矛盾（approved 卻帶著 revoked_at）。
func TestApprove_AfterRevokeClearsRevocation(t *testing.T) {
	s := newStore(t, nil, nil)
	_ = s.Approve("b", RoleUser, "a")
	_ = s.Revoke("b", "a")
	_ = s.Approve("b", RoleUser, "a")

	recs, _ := s.Records()
	if recs[0].Status != StatusApproved || recs[0].RevokedAt != "" || recs[0].RevokedBy != "" {
		t.Errorf("重新批准後不該殘留撤銷欄位: %+v", recs[0])
	}
}

func TestApprove_RejectsBadInput(t *testing.T) {
	s := newStore(t, nil, nil)
	if err := s.Approve("", RoleUser, "a"); err == nil {
		t.Error("空條目應被拒")
	}
	if err := s.Approve("b", "superuser", "a"); err == nil {
		t.Error("未知角色應被拒")
	}
}

// 快取要真的命中，不只是「結果一致」。驗法：建好快取後把檔案設為不可讀——os.Stat 仍成功
// （指紋不變），但 os.ReadFile 會失敗。若仍回得出資料，就證明沒有重讀檔。
func TestRecords_UsesCacheWhenUnchanged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 無視檔案權限，本驗法不成立")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	s := New(dir, nil, nil)
	if err := s.Approve("b", RoleUser, "a"); err != nil {
		t.Fatal(err)
	}
	if recs, _ := s.Records(); len(recs) != 1 {
		t.Fatalf("前置條件：應有 1 筆，得到 %d", len(recs))
	}

	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	recs, err := s.Records()
	if err != nil || len(recs) != 1 || recs[0].Entry != "b" {
		t.Fatalf("指紋未變時應命中快取、不重讀檔: err=%v recs=%v", err, recs)
	}

	// 回傳的是複本：改它不該污染快取。
	recs[0].Entry = "tampered"
	if again, _ := s.Records(); again[0].Entry != "b" {
		t.Error("Records 應回複本，呼叫端的修改不可污染快取")
	}
}

// 快取【不能】犧牲即時性：別的行程改了檔，下一次查詢就要看到——撤銷不能等 TTL。
func TestRecords_CacheInvalidatesOnExternalWrite(t *testing.T) {
	dir := t.TempDir()
	reader := New(dir, nil, nil)
	writer := New(dir, nil, nil) // 模擬另一個行程（面板）持有自己的 Store

	if err := writer.Approve("b", RoleUser, "a"); err != nil {
		t.Fatal(err)
	}
	if allowed, _, _ := reader.Sets(); !allowed["b"] {
		t.Fatal("前置條件：reader 應看得到 b")
	}

	// 外部撤銷後，reader 不重啟、不清快取，下一次查詢就該失效。
	if err := writer.Revoke("b", "a"); err != nil {
		t.Fatal(err)
	}
	if allowed, _, _ := reader.Sets(); allowed["b"] {
		t.Error("外部行程撤銷後，讀取端應立刻失效——撤銷若要等 TTL，等於沒有撤銷")
	}
}

// 壞檔不進快取：修好之後下一次查詢就該恢復，不必重啟。
func TestRecords_BrokenFileNotCached(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{ bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(dir, nil, nil)
	if _, err := s.Records(); err == nil {
		t.Fatal("前置條件：壞檔應報錯")
	}
	if err := os.WriteFile(path, []byte(`{"users":[{"entry":"b","role":"user","status":"approved"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := s.Records()
	if err != nil || len(recs) != 1 {
		t.Errorf("檔案修好後應立刻恢復（壞檔不該被快取住）: %v %d", err, len(recs))
	}
}
