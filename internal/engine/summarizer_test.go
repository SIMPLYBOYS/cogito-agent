package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// summarizeFake 是可控回覆 + 記錄最後一次 user prompt 的假 provider。
type summarizeFake struct {
	reply    string
	lastUser string
	calls    int
}

func (f *summarizeFake) Generate(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	f.calls++
	for _, m := range msgs {
		if m.Role == schema.RoleUser {
			f.lastUser = m.Content
		}
	}
	return &schema.Message{Role: schema.RoleAssistant, Content: f.reply}, nil
}
func (f *summarizeFake) MaxContextTokens() int { return 200000 }
func (f *summarizeFake) ModelName() string     { return "fake" }

func fillSession(n int) *ctxpkg.Session {
	s := ctxpkg.NewSession("t", "/tmp")
	for i := 0; i < n; i++ {
		role := schema.RoleUser
		if i%2 == 1 {
			role = schema.RoleAssistant
		}
		s.Append(schema.Message{Role: role, Content: fmt.Sprintf("msg-%d", i)})
	}
	return s
}

func historyLen(s *ctxpkg.Session) int { return len(s.GetWorkingMemory(1 << 20)) }

// 超過門檻 → 摺疊出摘要，且 history 真正收斂到末 N 條逐字（記憶體有界）。
func TestMaintainSummary_EvictsAndBounds(t *testing.T) {
	f := &summarizeFake{reply: "ROLL-1"}
	e := &AgentEngine{provider: f, EnableSummary: true}
	s := fillSession(45)

	e.maintainSummary(context.Background(), s)

	if f.calls != 1 {
		t.Fatalf("應恰好 1 次摘要 LLM 呼叫，got %d", f.calls)
	}
	if s.Summary() != "ROLL-1" {
		t.Errorf("摘要應被設定，got %q", s.Summary())
	}
	if got := historyLen(s); got != summaryTailMsgs {
		t.Errorf("history 應收斂到末 %d 條，got %d", summaryTailMsgs, got)
	}
}

// 未達門檻 → 完全不動作（多數短對話零成本、零 LLM）。
func TestMaintainSummary_BelowTriggerNoop(t *testing.T) {
	f := &summarizeFake{reply: "X"}
	e := &AgentEngine{provider: f, EnableSummary: true}
	s := fillSession(30) // < summaryTriggerMsgs(40)

	e.maintainSummary(context.Background(), s)

	if f.calls != 0 {
		t.Errorf("未達門檻不該呼叫 LLM，got %d", f.calls)
	}
	if s.Summary() != "" || historyLen(s) != 30 {
		t.Errorf("未達門檻不該變動：summary=%q len=%d", s.Summary(), historyLen(s))
	}
}

// 關閉開關 → 即便超門檻也不動作（bench/一次性任務的確定性）。
func TestMaintainSummary_DisabledNoop(t *testing.T) {
	f := &summarizeFake{reply: "X"}
	e := &AgentEngine{provider: f, EnableSummary: false}
	s := fillSession(50)

	e.maintainSummary(context.Background(), s)

	if f.calls != 0 || s.Summary() != "" || historyLen(s) != 50 {
		t.Errorf("關閉時不該動作：calls=%d summary=%q len=%d", f.calls, s.Summary(), historyLen(s))
	}
}

// 增量：第二次摺疊要把「先前摘要」帶進 prompt（滾動合併而非從頭重摘）。
func TestMaintainSummary_IncrementalCarriesPrev(t *testing.T) {
	f := &summarizeFake{reply: "PREV-SUMMARY"}
	e := &AgentEngine{provider: f, EnableSummary: true}
	s := fillSession(45)
	e.maintainSummary(context.Background(), s) // 第一次：prev 為空

	if strings.Contains(f.lastUser, "先前摘要") {
		t.Error("第一次摺疊不該有先前摘要段")
	}
	for i := 0; i < 25; i++ { // 再撐過門檻
		s.Append(schema.Message{Role: schema.RoleUser, Content: fmt.Sprintf("more-%d", i)})
	}
	e.maintainSummary(context.Background(), s) // 第二次：prev = PREV-SUMMARY

	if f.calls != 2 {
		t.Fatalf("應 2 次摘要呼叫，got %d", f.calls)
	}
	if !strings.Contains(f.lastUser, "先前摘要") || !strings.Contains(f.lastUser, "PREV-SUMMARY") {
		t.Errorf("第二次摺疊應帶入先前摘要，prompt=%q", f.lastUser)
	}
}

func TestClampRunes_CJKSafe(t *testing.T) {
	s := strings.Repeat("測試中文", 500) // 2000 runes
	got := clampRunes(s, 100)
	if r := []rune(got); len(r) > 130 { // 100 + 標記
		t.Errorf("應截到約 100 字，got %d", len(r))
	}
	if !strings.Contains(got, "截斷") {
		t.Errorf("應含截斷標記，got %q", got)
	}
}
