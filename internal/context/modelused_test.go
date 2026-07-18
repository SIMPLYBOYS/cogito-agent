package context

import "testing"

// SetModelUsed 記實際模型、落地；空字串 no-op、不覆蓋。
func TestSession_SetModelUsed(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	GlobalSessionMgr.SetStore(store)
	s := GlobalSessionMgr.GetOrCreate("mu-test", dir)

	s.SetModelUsed("claude-haiku-4-5")
	snap, ok, _ := store.Load("mu-test")
	if !ok || snap.ModelUsed != "claude-haiku-4-5" {
		t.Fatalf("SetModelUsed 應落地 model_used，got ok=%v model_used=%q", ok, snap.ModelUsed)
	}

	s.SetModelUsed("") // 空＝no-op
	snap2, _, _ := store.Load("mu-test")
	if snap2.ModelUsed != "claude-haiku-4-5" {
		t.Errorf("空字串不該覆蓋，got %q", snap2.ModelUsed)
	}
}
