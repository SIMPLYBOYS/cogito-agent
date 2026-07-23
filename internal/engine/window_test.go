package engine

import (
	"context"
	"fmt"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// capturingProvider 記錄每次 Generate 收到的訊息數，並立即回最終回答（不進工具迴圈）。
type capturingProvider struct{ msgLens []int }

func (c *capturingProvider) Generate(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	c.msgLens = append(c.msgLens, len(msgs))
	return &schema.Message{Role: schema.RoleAssistant, Content: "完成"}, nil
}
func (c *capturingProvider) MaxContextTokens() int { return 200000 }
func (c *capturingProvider) ModelName() string     { return "capture" }

// 錨定式窗口（caching 斷點③的前提）：EnableSummary 開＝吃全量（history 由逐出機制有界，
// 訊息序列 append-only、前綴穩定）；關＝維持滑窗（防 history 無界）。
func TestRun_WindowAnchoredWhenSummaryOn(t *testing.T) {
	build := func(n int) *ctxpkg.Session {
		sess := ctxpkg.NewSession(fmt.Sprintf("win-%d", n), t.TempDir())
		for i := 0; i < n; i++ { // 合法交替的長歷史（> summaryTailMsgs、< summaryTriggerMsgs）
			if i%2 == 0 {
				sess.Append(schema.Message{Role: schema.RoleUser, Content: fmt.Sprintf("問 %d", i)})
			} else {
				sess.Append(schema.Message{Role: schema.RoleAssistant, Content: fmt.Sprintf("答 %d", i)})
			}
		}
		return sess
	}
	const n = 30 // 20 < 30 < 40：滑窗會截、逐出不會觸發——正是兩種模式分岔的區間

	// EnableSummary 開 → 全量：30 則 + system ＝ 31
	on := &capturingProvider{}
	engOn := NewAgentEngine(on, newTestRegistry(), false, false)
	engOn.EnableSummary = true
	if err := engOn.Run(context.Background(), build(n), nil); err != nil {
		t.Fatal(err)
	}
	if len(on.msgLens) == 0 || on.msgLens[0] != n+1 {
		t.Errorf("summary 開應吃全量（%d+system=%d），實得 %v", n, n+1, on.msgLens)
	}

	// EnableSummary 關 → 滑窗末 summaryTailMsgs 則（+system；頭部若非 user 另有佔位符）
	off := &capturingProvider{}
	engOff := NewAgentEngine(off, newTestRegistry(), false, false)
	engOff.EnableSummary = false
	if err := engOff.Run(context.Background(), build(n), nil); err != nil {
		t.Fatal(err)
	}
	if len(off.msgLens) == 0 || off.msgLens[0] > summaryTailMsgs+2 {
		t.Errorf("summary 關應維持滑窗（≤%d+system+佔位），實得 %v", summaryTailMsgs, off.msgLens)
	}
	if off.msgLens[0] >= on.msgLens[0] {
		t.Errorf("滑窗（%d）應小於全量（%d）", off.msgLens[0], on.msgLens[0])
	}
}
