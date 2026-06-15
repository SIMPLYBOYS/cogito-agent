package provider

import (
	"testing"

	"github.com/yourname/go-tiny-claw/internal/schema"
)

// 驗證 Anthropic 的 user/assistant 嚴格交替不變式：一個完整 ReAct 回合 + 併發多工具結果
// + ch15 死循環提醒（普通 user 文本緊跟 tool_result）。這些歷史結構若映射不當會觸發 400。
func TestBuildAnthropicMessages_StrictAlternation(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "you are claw"},
		{Role: schema.RoleUser, Content: "do it"},
		{Role: schema.RoleAssistant, Content: "ok", ToolCalls: []schema.ToolCall{
			{ID: "t1", Name: "read_file", Arguments: []byte(`{"path":"a"}`)},
			{ID: "t2", Name: "read_file", Arguments: []byte(`{"path":"b"}`)},
		}},
		{Role: schema.RoleUser, Content: "resultA", ToolCallID: "t1"}, // 併發工具結果 1
		{Role: schema.RoleUser, Content: "resultB", ToolCallID: "t2"}, // 併發工具結果 2
		{Role: schema.RoleUser, Content: "[SYSTEM REMINDER] stop"},    // ch15 提醒：普通 user 文本
	}

	out, system := buildAnthropicMessages(msgs)

	if system != "you are claw" {
		t.Errorf("system 未正確抽出: %q", system)
	}

	// 期望 3 條：user(do it) / assistant(text+tool_use) / user(2×tool_result + 提醒文本)
	if len(out) != 3 {
		t.Fatalf("期望 3 條消息，實際 %d 條", len(out))
	}

	wantRoles := []string{"user", "assistant", "user"}
	for i, m := range out {
		if string(m.Role) != wantRoles[i] {
			t.Errorf("第 %d 條 role=%q，期望 %q", i, m.Role, wantRoles[i])
		}
	}

	// 嚴格交替不變式：相鄰兩條 role 必不同（連續兩條 user 就會被 Anthropic 拒絕）
	for i := 1; i < len(out); i++ {
		if out[i].Role == out[i-1].Role {
			t.Errorf("位置 %d/%d 出現連續相同 role %q —— 違反 Anthropic 交替要求", i-1, i, out[i].Role)
		}
	}

	// 最後一條 user 應包含 2 個 tool_result 塊 + 1 個 text 塊（提醒被併入同一回合）
	var toolResults, texts int
	for _, b := range out[2].Content {
		if b.OfToolResult != nil {
			toolResults++
		}
		if b.OfText != nil {
			texts++
		}
	}
	if toolResults != 2 {
		t.Errorf("最後一條 user 應有 2 個 tool_result 塊，實際 %d", toolResults)
	}
	if texts != 1 {
		t.Errorf("最後一條 user 應有 1 個 text 塊（ch15 提醒併入），實際 %d", texts)
	}
}

// 單工具結果在末尾時也應正確 flush，不丟失。
func TestBuildAnthropicMessages_TrailingToolResultFlushed(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "go"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "t1", Name: "bash", Arguments: []byte(`{"command":"ls"}`)},
		}},
		{Role: schema.RoleUser, Content: "files...", ToolCallID: "t1"},
	}

	out, _ := buildAnthropicMessages(msgs)

	if len(out) != 3 {
		t.Fatalf("期望 3 條消息，實際 %d 條（末尾 tool_result 可能漏 flush）", len(out))
	}
	if string(out[2].Role) != "user" {
		t.Errorf("末條應為 user(tool_result)，實際 %q", out[2].Role)
	}
}
