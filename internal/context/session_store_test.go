package context

import (
	"strings"
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

// 引擎先 Append(assistant+tool_calls) 再執行工具、最後 Append(tool_results)。行程在這兩者之間
// 被砍（/stop、崩潰），history 就留下懸空的 tool_use——下一則使用者訊息接上去後，Anthropic 回
// 400「tool_use ids were found without tool_result blocks」，且【每次】後續請求都會撞同一個錯，
// 該 session 等於永久磚化。實際發生過（2026-07-22 排練）。
func TestGetWorkingMemory_DropsDanglingToolUse(t *testing.T) {
	s := NewSession("dangling", t.TempDir())
	s.Append(
		schema.Message{Role: schema.RoleUser, Content: "跑一下"},
		schema.Message{Role: schema.RoleAssistant, Content: "我來查", ToolCalls: []schema.ToolCall{
			{ID: "orphan1", Name: "bash", Arguments: []byte("{}")},
		}}, // ← 工具結果從未寫入（行程被砍）
		schema.Message{Role: schema.RoleUser, Content: "hi"},
	)

	got := s.GetWorkingMemory(20)

	for _, m := range got {
		for _, tc := range m.ToolCalls {
			if tc.ID == "orphan1" {
				t.Fatalf("懸空的 tool_use 應被剝除，否則送出即 400")
			}
		}
	}
	// 只拿掉呼叫、保留 thinking 文字——那段上下文有價值，沒理由連坐丟棄
	var kept bool
	for _, m := range got {
		if m.Role == schema.RoleAssistant && strings.Contains(m.Content, "我來查") {
			kept = true
			if len(m.ToolCalls) != 0 {
				t.Error("該則的 ToolCalls 應被清空")
			}
		}
	}
	if !kept {
		t.Error("thinking 文字不該被連坐丟棄")
	}
}

// 正常配對的 tool_use 不可被誤傷。
func TestGetWorkingMemory_KeepsPairedToolUse(t *testing.T) {
	s := NewSession("paired", t.TempDir())
	s.Append(
		schema.Message{Role: schema.RoleUser, Content: "跑一下"},
		schema.Message{Role: schema.RoleAssistant, Content: "我來查", ToolCalls: []schema.ToolCall{
			{ID: "ok1", Name: "bash", Arguments: []byte("{}")},
		}},
		schema.Message{Role: schema.RoleUser, ToolCallID: "ok1", Content: "結果"},
	)
	for _, m := range s.GetWorkingMemory(20) {
		for _, tc := range m.ToolCalls {
			if tc.ID == "ok1" {
				return // 有配對，保留正確
			}
		}
	}
	t.Fatal("配對完整的 tool_use 被誤刪了")
}
