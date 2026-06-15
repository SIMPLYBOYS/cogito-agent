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
	// 同工具不同參數：指紋各自獨立（都沒到 3）、同工具計數=3（沒到 5），均不觸發
	r.CheckAndInject(mkCall("read_file", `{"path":"a"}`), errResult())
	r.CheckAndInject(mkCall("read_file", `{"path":"b"}`), errResult())
	if got := r.CheckAndInject(mkCall("read_file", `{"path":"a"}`), errResult()); got != nil {
		t.Fatal("不同參數不應累加到同一指紋（path a 僅失敗 2 次），且同工具僅 3 次 <5，不應觸發")
	}
}

// 修補②：模型「每次微調參數盲目試錯」會繞過指紋探測——同一工具(任意參數)
// 連續失敗達 sameToolThreshold(5) 次必須被攔截。
func TestReminderInjector_VaryingArgsTripsSameToolThreshold(t *testing.T) {
	r := NewReminderInjector()
	for i, p := range []string{"a", "b", "c", "d"} {
		if got := r.CheckAndInject(mkCall("read_file", `{"path":"`+p+`"}`), errResult()); got != nil {
			t.Fatalf("第 %d 次不同參數失敗不應觸發（同工具仍 <5）", i+1)
		}
	}
	got := r.CheckAndInject(mkCall("read_file", `{"path":"e"}`), errResult())
	if got == nil {
		t.Fatal("同一工具連續 5 次失敗（即便每次參數不同）應觸發死循環提醒")
	}
	if got.Role != schema.RoleUser || !strings.Contains(got.Content, "SYSTEM REMINDER") {
		t.Errorf("提醒消息格式不對: role=%q content=%q", got.Role, got.Content)
	}
}

// CheckTurn 應把本輪所有並行工具結果都納入計數（而非只看第一個）。
func TestReminderInjector_CheckTurnCountsAllParallelTools(t *testing.T) {
	r := NewReminderInjector()
	c := mkCall("bash", `{"command":"x"}`)
	// 每輪兩個相同失敗調用：第 1 輪→計數 2，第 2 輪達到 3，CheckTurn 應返回提醒
	calls := []schema.ToolCall{c, c}
	results := []schema.ToolResult{errResult(), errResult()}
	if got := r.CheckTurn(calls, results); got != nil {
		t.Fatal("首輪同指紋累計 2 次，不應觸發")
	}
	if got := r.CheckTurn(calls, results); got == nil {
		t.Fatal("第二輪累計達 ≥3 次，CheckTurn 應返回提醒（證明並行工具都被計入）")
	}
}
