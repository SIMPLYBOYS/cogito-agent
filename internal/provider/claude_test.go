package provider

import (
	"testing"

	"github.com/yourname/go-tiny-claw/internal/schema"
)

// 验证 Anthropic 的 user/assistant 严格交替不变式：一个完整 ReAct 回合 + 并发多工具结果
// + ch15 死循环提醒（普通 user 文本紧跟 tool_result）。这些历史结构若映射不当会触发 400。
func TestBuildAnthropicMessages_StrictAlternation(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "you are claw"},
		{Role: schema.RoleUser, Content: "do it"},
		{Role: schema.RoleAssistant, Content: "ok", ToolCalls: []schema.ToolCall{
			{ID: "t1", Name: "read_file", Arguments: []byte(`{"path":"a"}`)},
			{ID: "t2", Name: "read_file", Arguments: []byte(`{"path":"b"}`)},
		}},
		{Role: schema.RoleUser, Content: "resultA", ToolCallID: "t1"}, // 并发工具结果 1
		{Role: schema.RoleUser, Content: "resultB", ToolCallID: "t2"}, // 并发工具结果 2
		{Role: schema.RoleUser, Content: "[SYSTEM REMINDER] stop"},    // ch15 提醒：普通 user 文本
	}

	out, system := buildAnthropicMessages(msgs)

	if system != "you are claw" {
		t.Errorf("system 未正确抽出: %q", system)
	}

	// 期望 3 条：user(do it) / assistant(text+tool_use) / user(2×tool_result + 提醒文本)
	if len(out) != 3 {
		t.Fatalf("期望 3 条消息，实际 %d 条", len(out))
	}

	wantRoles := []string{"user", "assistant", "user"}
	for i, m := range out {
		if string(m.Role) != wantRoles[i] {
			t.Errorf("第 %d 条 role=%q，期望 %q", i, m.Role, wantRoles[i])
		}
	}

	// 严格交替不变式：相邻两条 role 必不同（连续两条 user 就会被 Anthropic 拒绝）
	for i := 1; i < len(out); i++ {
		if out[i].Role == out[i-1].Role {
			t.Errorf("位置 %d/%d 出现连续相同 role %q —— 违反 Anthropic 交替要求", i-1, i, out[i].Role)
		}
	}

	// 最后一条 user 应包含 2 个 tool_result 块 + 1 个 text 块（提醒被并入同一回合）
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
		t.Errorf("最后一条 user 应有 2 个 tool_result 块，实际 %d", toolResults)
	}
	if texts != 1 {
		t.Errorf("最后一条 user 应有 1 个 text 块（ch15 提醒并入），实际 %d", texts)
	}
}

// 单工具结果在末尾时也应正确 flush，不丢失。
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
		t.Fatalf("期望 3 条消息，实际 %d 条（末尾 tool_result 可能漏 flush）", len(out))
	}
	if string(out[2].Role) != "user" {
		t.Errorf("末条应为 user(tool_result)，实际 %q", out[2].Role)
	}
}
