package context

import (
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func newTestManager(store SessionStore) *SessionManager {
	sm := &SessionManager{sessions: make(map[string]*Session)}
	sm.SetStore(store)
	return sm
}

func TestFileSessionStore_RoundTrip(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snap := &SessionSnapshot{
		ID:           "chan-123",
		WorkDir:      "/w",
		CreatedAt:    "2026-06-23T10:00:00Z",
		UpdatedAt:    "2026-06-23T10:05:00Z",
		History:      []schema.Message{{Role: schema.RoleUser, Content: "hi"}},
		TotalCostUSD: 0.42,
	}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	got, found, err := store.Load("chan-123")
	if err != nil || !found {
		t.Fatalf("Load 失敗: found=%v err=%v", found, err)
	}
	if got.ID != "chan-123" || got.TotalCostUSD != 0.42 || len(got.History) != 1 || got.History[0].Content != "hi" {
		t.Errorf("round-trip 內容不符: %+v", got)
	}
}

func TestFileSessionStore_LoadMissing(t *testing.T) {
	store, _ := NewFileSessionStore(t.TempDir())
	if _, found, err := store.Load("nope"); err != nil || found {
		t.Errorf("不存在的 session 應 found=false、err=nil，got found=%v err=%v", found, err)
	}
}

// 模擬「重啟」：mgr1 寫入並落盤後，全新的 mgr2 用同一 store 目錄應能復原歷史與費用。
func TestSessionManager_RehydrateAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	store1, _ := NewFileSessionStore(dir)
	mgr1 := newTestManager(store1)

	s := mgr1.GetOrCreate("task-1", "/w")
	s.Append(schema.Message{Role: schema.RoleUser, Content: "第一步"})
	s.Append(schema.Message{Role: schema.RoleAssistant, Content: "好的"})
	s.RecordUsage(100, 50, 0.01)

	// 「重啟」：新 store + 新 manager，指向同一磁碟目錄
	store2, _ := NewFileSessionStore(dir)
	mgr2 := newTestManager(store2)

	got := mgr2.GetOrCreate("task-1", "/w")
	hist := got.GetWorkingMemory(0)
	if len(hist) != 2 || hist[0].Content != "第一步" || hist[1].Content != "好的" {
		t.Fatalf("歷史未復原: %+v", hist)
	}
	if got.CostUSD() != 0.01 {
		t.Errorf("費用未復原: got %v want 0.01", got.CostUSD())
	}
}

func TestSession_NoStore_NoPanic(t *testing.T) {
	// store 為 nil（純記憶體）時 Append/RecordUsage 不應 panic
	mgr := &SessionManager{sessions: make(map[string]*Session)}
	s := mgr.GetOrCreate("mem", "/w")
	s.Append(schema.Message{Role: schema.RoleUser, Content: "x"})
	s.RecordUsage(1, 1, 0.001)
	if len(s.GetWorkingMemory(0)) != 1 {
		t.Error("純記憶體模式應正常運作")
	}
}
