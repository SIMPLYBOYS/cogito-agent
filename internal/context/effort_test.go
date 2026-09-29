package context

import "testing"

// 頻道的思考力度覆蓋：落地、重啟讀得回來、空字串＝清掉。
func TestSession_EffortPersists(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	GlobalSessionMgr.SetStore(store)
	s := GlobalSessionMgr.GetOrCreate("effort-test", dir)

	s.SetEffort("xhigh")
	snap, ok, _ := store.Load("effort-test")
	if !ok || snap.Effort != "xhigh" {
		t.Fatalf("SetEffort 應落地，got ok=%v effort=%q", ok, snap.Effort)
	}
	if got := newSessionFromSnapshot(snap, store).Effort(); got != "xhigh" {
		t.Errorf("重啟（從快照還原）後讀不回來：%q", got)
	}
	s.SetEffort("")
	if snap2, _, _ := store.Load("effort-test"); snap2.Effort != "" {
		t.Errorf("空字串要清掉，got %q", snap2.Effort)
	}
}
