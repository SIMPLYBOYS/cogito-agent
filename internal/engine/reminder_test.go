package engine

import (
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
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
		t.Fatalf("第 1 次失敗不應觸發，卻回傳: %v", got)
	}
	if got := r.CheckAndInject(c, errResult()); got != nil {
		t.Fatalf("第 2 次失敗不應觸發")
	}
	got := r.CheckAndInject(c, errResult())
	if got == nil {
		t.Fatal("第 3 次相同參數失敗應觸發無窮迴圈提醒，卻回傳 nil")
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
		t.Fatal("同一工具連續 5 次失敗（即便每次參數不同）應觸發無窮迴圈提醒")
	}
	if got.Role != schema.RoleUser || !strings.Contains(got.Content, "SYSTEM REMINDER") {
		t.Errorf("提醒消息格式不對: role=%q content=%q", got.Role, got.Content)
	}
}

// 指紋正規化：本質相同、只差微小寫法的參數應產生【同一】指紋。
func TestFingerprint_NormalizesTrivialDiffs(t *testing.T) {
	same := [][2]string{
		{`{"path":"/tmp/a.txt"}`, `{"path":"/tmp/a.txt "}`},  // 尾空格
		{`{"path":"/tmp/a.txt"}`, `{"path":"/tmp/./a.txt"}`}, // 冗餘 ./
		{`{"path":"/tmp/a.txt"}`, `{"path":"/tmp//a.txt"}`},  // 雙斜線
		{`{"a":"1","b":"2"}`, `{"b":"2","a":"1"}`},           // key 順序
		{`{"command":"ls -la"}`, `{"command":"ls -la "}`},    // command 尾空格
	}
	for _, p := range same {
		if generateFingerprint("read_file", []byte(p[0])) != generateFingerprint("read_file", []byte(p[1])) {
			t.Errorf("應視為同一指紋: %s vs %s", p[0], p[1])
		}
	}
}

// 反面：本質不同的參數不應被過度正規化而誤併。
func TestFingerprint_DoesNotOverNormalize(t *testing.T) {
	diff := [][2]string{
		{`{"path":"/tmp/a.txt"}`, `{"path":"/tmp/b.txt"}`},     // 不同檔
		{`{"path":"/tmp/a.txt"}`, `{"path":"/tmp/A.txt"}`},     // 大小寫敏感
		{`{"path":"/tmp/a.txt"}`, `{"path":"./../tmp/a.txt"}`}, // 相對 vs 絕對：語意不同，刻意不併
		{`{"command":"ls -la"}`, `{"command":"ls  -la"}`},      // command 內部空白不折疊（保守）
	}
	for _, p := range diff {
		if generateFingerprint("read_file", []byte(p[0])) == generateFingerprint("read_file", []byte(p[1])) {
			t.Errorf("不應併為同一指紋: %s vs %s", p[0], p[1])
		}
	}
}

// 端到端：三次「微差」重試（規範化後同一目標）應在第 3 次觸發無窮迴圈，不再被尾空格/冗餘 ./ 繞過。
func TestReminderInjector_TrivialDiffRetriesTripAtThree(t *testing.T) {
	r := NewReminderInjector()
	calls := []string{
		`{"path":"/tmp/a.txt"}`,
		`{"path":"/tmp/a.txt "}`,
		`{"path":"/tmp/./a.txt"}`,
	}
	var last *schema.Message
	for _, a := range calls {
		last = r.CheckAndInject(mkCall("read_file", a), errResult())
	}
	if last == nil {
		t.Fatal("三次微差重試（規範化後同一目標）應在第 3 次觸發無窮迴圈提醒")
	}
}

// CheckTurn 應把本輪所有並行工具結果都納入計數（而非只看第一個）。
func TestReminderInjector_CheckTurnCountsAllParallelTools(t *testing.T) {
	r := NewReminderInjector()
	c := mkCall("bash", `{"command":"x"}`)
	// 每輪兩個相同失敗呼叫：第 1 輪→計數 2，第 2 輪達到 3，CheckTurn 應回傳提醒
	calls := []schema.ToolCall{c, c}
	results := []schema.ToolResult{errResult(), errResult()}
	if got := r.CheckTurn(calls, results); got != nil {
		t.Fatal("首輪同指紋累計 2 次，不應觸發")
	}
	if got := r.CheckTurn(calls, results); got == nil {
		t.Fatal("第二輪累計達 ≥3 次，CheckTurn 應回傳提醒（證明並行工具都被計入）")
	}
}
