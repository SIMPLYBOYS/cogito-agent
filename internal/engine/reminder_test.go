package engine

import (
	"strings"
	"testing"

	"github.com/yourname/go-tiny-claw/internal/schema"
)

func mkCall(name, args string) schema.ToolCall {
	return schema.ToolCall{Name: name, Arguments: []byte(args)}
}

func errResult() schema.ToolResult { return schema.ToolResult{IsError: true, Output: "boom"} }
func okResult() schema.ToolResult  { return schema.ToolResult{IsError: false, Output: "ok"} }

func TestReminderInjector_TriggersAfterThreeIdenticalFailures(t *testing.T) {
	r := NewReminderInjector()
	c := mkCall("read_file", `{"path":"x"}`)

	if got := r.CheckAndInject(c, errResult()); got != nil {
		t.Fatalf("第 1 次失败不应触发，却返回: %v", got)
	}
	if got := r.CheckAndInject(c, errResult()); got != nil {
		t.Fatalf("第 2 次失败不应触发")
	}
	got := r.CheckAndInject(c, errResult())
	if got == nil {
		t.Fatal("第 3 次相同参数失败应触发死循环提醒，却返回 nil")
	}
	if got.Role != schema.RoleUser || !strings.Contains(got.Content, "SYSTEM REMINDER") {
		t.Errorf("提醒消息格式不对: role=%q content=%q", got.Role, got.Content)
	}
}

func TestReminderInjector_SuccessResetsCounter(t *testing.T) {
	r := NewReminderInjector()
	c := mkCall("bash", `{"command":"foo"}`)

	r.CheckAndInject(c, errResult())
	r.CheckAndInject(c, errResult())
	if got := r.CheckAndInject(c, okResult()); got != nil {
		t.Fatal("成功不应触发提醒")
	}
	// 计数已清零：再失败两次仍不触发
	r.CheckAndInject(c, errResult())
	if got := r.CheckAndInject(c, errResult()); got != nil {
		t.Fatal("成功清零后，仅 2 次失败不应触发")
	}
}

func TestReminderInjector_DifferentArgsDoNotAccumulate(t *testing.T) {
	r := NewReminderInjector()
	// 同工具不同参数：各自独立指纹计数，都没到 3 次
	r.CheckAndInject(mkCall("read_file", `{"path":"a"}`), errResult())
	r.CheckAndInject(mkCall("read_file", `{"path":"b"}`), errResult())
	if got := r.CheckAndInject(mkCall("read_file", `{"path":"a"}`), errResult()); got != nil {
		t.Fatal("不同参数不应累加到同一指纹（path a 仅失败 2 次），不应触发")
	}
}
