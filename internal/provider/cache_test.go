package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// 斷點③（對話尾端）：多輪對話（≥minMsgsForConvoCache 則）在最後一則訊息的最後一個 block 掛
// ephemeral；一次性呼叫（1~2 則）不掛（純寫入稅）。以 JSON 序列化黑箱驗證，不綁 SDK 內部欄位。
func TestBuildParams_ConvoCacheBreakpoint(t *testing.T) {
	p := &ClaudeProvider{model: "claude-test"}
	tools := []schema.ToolDefinition{{Name: "bash", InputSchema: map[string]interface{}{}}}

	marshal := func(msgs []schema.Message) string {
		b, err := json.Marshal(p.buildParams(msgs, tools))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	// cache_control 出現次數（含 tools/system 上的既有兩個斷點）
	count := func(s string) int { return strings.Count(s, `"cache_control"`) }

	// 一次性呼叫（system+user → 1 則 anthropic 訊息）：只有 tools+system 兩個斷點
	oneShot := []schema.Message{
		{Role: schema.RoleSystem, Content: "你是評審"},
		{Role: schema.RoleUser, Content: "判斷這段"},
	}
	if got := count(marshal(oneShot)); got != 2 {
		t.Errorf("一次性呼叫應只有 tools+system 兩個斷點，得到 %d", got)
	}

	// 多輪對話（3 則）：多一個對話尾端斷點 → 共 3；且掛在【最後一則】訊息內
	convo := []schema.Message{
		{Role: schema.RoleSystem, Content: "你是 agent"},
		{Role: schema.RoleUser, Content: "任務"},
		{Role: schema.RoleAssistant, Content: "先看檔", ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "c1", Content: "a.txt b.txt"},
	}
	out := marshal(convo)
	if got := count(out); got != 3 {
		t.Errorf("多輪對話應為 3 個斷點（tools/system/對話尾端），得到 %d", got)
	}
	// 尾端斷點應在最後一個 tool_result block 上：檢查最後一次 cache_control 出現在最後一個
	// tool_result 之後（序列化順序＝messages 順序）
	lastCC := strings.LastIndex(out, `"cache_control"`)
	lastTR := strings.LastIndex(out, `"tool_result"`)
	if lastTR < 0 || lastCC < lastTR {
		t.Errorf("對話尾端斷點應掛在最後的 tool_result block 上（lastCC=%d lastTR=%d）", lastCC, lastTR)
	}

	// 斷點總數永遠 ≤ Anthropic 上限 4
	if got := count(out); got > 4 {
		t.Errorf("斷點數 %d 超過 Anthropic 上限 4", got)
	}

	// 尾端是純文字 assistant 的情況也掛得上（text block 變體）
	textEnd := []schema.Message{
		{Role: schema.RoleSystem, Content: "s"},
		{Role: schema.RoleUser, Content: "q"},
		{Role: schema.RoleAssistant, Content: "想法"},
		{Role: schema.RoleUser, Content: "繼續"},
	}
	if got := count(marshal(textEnd)); got != 3 {
		t.Errorf("text 結尾的多輪對話也應有 3 個斷點，得到 %d", got)
	}
}
