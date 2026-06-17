package context

import (
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
)

// 一份含「早期大 tool_result」的上下文，壓縮時該結果（在保護區外）會被替換成佔位符。
func bigMsgs() []schema.Message {
	return []schema.Message{
		{Role: schema.RoleSystem, Content: "system prompt"},
		{Role: schema.RoleUser, Content: "task"},
		{Role: schema.RoleAssistant, Content: "calling tool"},
		{Role: schema.RoleUser, ToolCallID: "t1", Content: strings.Repeat("X", 6000)},
		{Role: schema.RoleAssistant, Content: "got it"},
		{Role: schema.RoleUser, Content: "next"},
		{Role: schema.RoleAssistant, Content: "done step"},
	}
}

// 自適應觸發：同一份內容，小窗口模型應觸發壓縮（變短），大窗口不應。
func TestCompactor_AdaptiveTriggerByWindow(t *testing.T) {
	msgs := bigMsgs()

	small := NewCompactor(1000, 0.75, 2) // 窗口 1000 → 水位 750 tokens；內容約 2000 tokens → 觸發
	out := small.Compact(msgs)
	if small.estimateLength(out) >= small.estimateLength(msgs) {
		t.Errorf("小窗口應觸發壓縮並變短：壓縮前 %d、後 %d 位元組",
			small.estimateLength(msgs), small.estimateLength(out))
	}

	large := NewCompactor(1_000_000, 0.75, 2) // 窗口 1M → 水位遠高於內容 → 不壓縮
	out2 := large.Compact(msgs)
	if len(out2) != len(msgs) || large.estimateLength(out2) != large.estimateLength(msgs) {
		t.Error("大窗口不應觸發壓縮，內容應原樣返回")
	}
}

// 自校準：餵入真實 (bytes, promptTokens) 後 byte/token 比下降，同內容的 token 估算應變大。
func TestCompactor_CalibrateShiftsEstimate(t *testing.T) {
	c := NewCompactor(200000, 0.75, 6)
	msgs := []schema.Message{{Role: schema.RoleUser, Content: strings.Repeat("x", 3000)}}

	before := c.estimatedTokens(msgs) // 預設 3.0 byte/token → 1000
	c.Calibrate(msgs, 3000)           // 量測 1.0；EWMA → 2.0
	after := c.estimatedTokens(msgs)  // 3000/2.0 → 1500

	if after <= before {
		t.Errorf("校準到更小的 byte/token 後，同內容 token 估算應變大：before=%d after=%d", before, after)
	}
}

// 異常 promptTokens（<=0）不應污染校準狀態。
func TestCompactor_CalibrateIgnoresInvalid(t *testing.T) {
	c := NewCompactor(200000, 0.75, 6)
	msgs := []schema.Message{{Role: schema.RoleUser, Content: strings.Repeat("x", 3000)}}
	before := c.estimatedTokens(msgs)
	c.Calibrate(msgs, 0)
	c.Calibrate(nil, 100)
	if got := c.estimatedTokens(msgs); got != before {
		t.Errorf("無效校準不應改變估算：before=%d after=%d", before, got)
	}
}

// 窗口未知（<=0）→ 不壓縮（行為等同關閉）。
func TestCompactor_UnknownWindowNoCompaction(t *testing.T) {
	c := NewCompactor(0, 0.75, 2)
	msgs := bigMsgs()
	out := c.Compact(msgs)
	if len(out) != len(msgs) || c.estimateLength(out) != c.estimateLength(msgs) {
		t.Error("窗口未知時不應壓縮")
	}
}
