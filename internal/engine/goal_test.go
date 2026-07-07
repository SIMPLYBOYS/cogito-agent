package engine

import (
	"context"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func TestExtractJSONObject(t *testing.T) {
	cases := map[string]string{
		`{"done":true}`:                        `{"done":true}`,
		"前言 {\"done\":false,\"r\":\"x\"} 後語": `{"done":false,"r":"x"}`,
		"```json\n{\"a\":{\"b\":1}}\n```":       `{"a":{"b":1}}`, // 巢狀 + 圍欄
		"沒有 json":                              "沒有 json",
	}
	for in, want := range cases {
		if got := extractJSONObject(in); got != want {
			t.Errorf("extractJSONObject(%q)=%q want %q", in, got, want)
		}
	}
}

func TestLastAssistantContent(t *testing.T) {
	s := ctxpkg.NewSession("g", t.TempDir())
	s.Append(schema.Message{Role: schema.RoleUser, Content: "task"})
	s.Append(schema.Message{Role: schema.RoleAssistant, Content: "第一版"})
	s.Append(schema.Message{Role: schema.RoleUser, ToolCallID: "1", Content: "工具結果"})
	s.Append(schema.Message{Role: schema.RoleAssistant, Content: "最終版"})
	if got := lastAssistantContent(s); got != "最終版" {
		t.Errorf("應取最後一則有內容的助手訊息，got %q", got)
	}
}

// jsonProvider 回傳固定文字（含 JSON），用來離線驗證 JudgeGoal 的解析。
type jsonProvider struct{ out string }

func (p jsonProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: p.out}, nil
}
func (jsonProvider) MaxContextTokens() int { return 200000 }
func (jsonProvider) ModelName() string     { return "json-model" }

func TestJudgeGoal(t *testing.T) {
	s := ctxpkg.NewSession("jg", t.TempDir())
	s.Append(schema.Message{Role: schema.RoleAssistant, Content: "我完成了 X"})

	eng := NewAgentEngine(jsonProvider{out: `判定如下：{"done": true, "reason": "X 已完成"}`}, newTestRegistry(), false, false)
	done, reason, err := eng.JudgeGoal(context.Background(), s, "X 是否完成")
	if err != nil {
		t.Fatal(err)
	}
	if !done || !strings.Contains(reason, "X 已完成") {
		t.Errorf("應判定達成，got done=%v reason=%q", done, reason)
	}

	// 無助手產出 → 未達成、不呼叫解析
	empty := ctxpkg.NewSession("jg2", t.TempDir())
	if done, _, _ := eng.JudgeGoal(context.Background(), empty, "任何標準"); done {
		t.Error("無產出時不該判定達成")
	}
}
