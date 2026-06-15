package engine

import (
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
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
		t.Fatalf("第 1 次失敗不應觸發，卻返回: %v", got)
	}
	if got := r.CheckAndInject(c, errResult()); got != nil {
		t.Fatalf("第 2 次失敗不應觸發")
	}
	got := r.CheckAndInject(c, errResult())
	if got == nil {
		t.Fatal("第 3 次相同參數失敗應觸發死循環提醒，卻返回 nil")
	}
	if got.Role != schema.RoleUser || !strings.Contains(got.Content, "SYSTEM REMINDER") {
		t.Errorf("提醒消息格式不對: role=%q content=%q", got.Role, got.Content)
	}
}

func TestReminderInjector_SuccessResetsCounter(t *testing.T) {
	r := NewReminderInjector()
	c := mkCall("bash", `{"command":"foo"}`)

	r.CheckAndInject(c, errResult())
	r.CheckAndInject(c, errResult())
	if got := r.CheckAndInject(c, okResult()); got != nil {
		t.Fatal("成功不應觸發提醒")
	}
	// 計數已清零：再失敗兩次仍不觸發
	r.CheckAndInject(c, errResult())
	if got := r.CheckAndInject(c, errResult()); got != nil {
		t.Fatal("成功清零後，僅 2 次失敗不應觸發")
	}
}

func TestReminderInjector_DifferentArgsDoNotAccumulate(t *testing.T) {
	r := NewReminderInjector()
	// 同工具不同參數：各自獨立指紋計數，都沒到 3 次
	r.CheckAndInject(mkCall("read_file", `{"path":"a"}`), errResult())
	r.CheckAndInject(mkCall("read_file", `{"path":"b"}`), errResult())
	if got := r.CheckAndInject(mkCall("read_file", `{"path":"a"}`), errResult()); got != nil {
		t.Fatal("不同參數不應累加到同一指紋（path a 僅失敗 2 次），不應觸發")
	}
}
