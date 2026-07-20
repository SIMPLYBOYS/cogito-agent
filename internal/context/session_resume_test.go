package context

import (
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// 跨重啟續跑的核心：重啟後只掃出「上次被硬砍、running=true」的 session，並保留其 WorkDir。
func TestListInterrupted_FindsOnlyRunning(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 行程 A：一個任務進行中被硬砍（running 落盤），一個正常閒置（running=false）。
	smA := &SessionManager{sessions: map[string]*Session{}}
	smA.SetStore(store)
	busy := smA.GetOrCreate("slack:C1", "/tmp/w1")
	busy.SetRunning(true)
	idle := smA.GetOrCreate("slack:C2", "/tmp/w2")
	idle.Append(schema.Message{Role: schema.RoleUser, Content: "hi"}) // 有歷史但 running=false

	// 行程 B（重啟）：全新 manager 綁同一 store。
	smB := &SessionManager{sessions: map[string]*Session{}}
	smB.SetStore(store)
	got := smB.ListInterrupted()

	if len(got) != 1 || got[0].ID != "slack:C1" {
		t.Fatalf("應只掃出 running 的 slack:C1，got %d 筆", len(got))
	}
	if got[0].WorkDir != "/tmp/w1" {
		t.Errorf("續跑時應保留快照裡的 WorkDir，got %q", got[0].WorkDir)
	}
	if !got[0].Running() {
		t.Error("掃出的 session 應仍標記 running")
	}
}

// 無持久化（store==nil）時沒有跨重啟續跑。
func TestListInterrupted_NoStoreNoResume(t *testing.T) {
	sm := &SessionManager{sessions: map[string]*Session{}}
	if got := sm.ListInterrupted(); got != nil {
		t.Errorf("無 store 應回 nil，got %d 筆", len(got))
	}
}

// ClearResume 與 Bump/Running 的狀態轉移（防崩潰迴圈的計數）。
func TestResumeState_Transitions(t *testing.T) {
	s := NewSession("x", "/tmp")
	s.SetRunning(true)
	s.BumpResumeAttempts()
	s.BumpResumeAttempts()
	if !s.Running() || s.ResumeAttempts() != 2 {
		t.Fatalf("前置狀態錯：running=%v attempts=%d", s.Running(), s.ResumeAttempts())
	}
	s.ClearResume() // 成功/放棄 → 歸零
	if s.Running() || s.ResumeAttempts() != 0 {
		t.Errorf("ClearResume 應把 running/attempts 歸零，got running=%v attempts=%d", s.Running(), s.ResumeAttempts())
	}
}
