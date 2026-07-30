package engine

import (
	"context"
	"reflect"
	"testing"
)

// recordingReporter 記下收到的事件序列。
type recordingReporter struct{ got []string }

func (r *recordingReporter) OnThinking(context.Context)      { r.got = append(r.got, "think") }
func (r *recordingReporter) OnTurn(_ context.Context, t int) { r.got = append(r.got, "turn") }
func (r *recordingReporter) OnToolCall(_ context.Context, n, a string) {
	r.got = append(r.got, "call:"+n)
}
func (r *recordingReporter) OnMessage(_ context.Context, c string) { r.got = append(r.got, "msg:"+c) }
func (r *recordingReporter) OnToolResult(_ context.Context, n, res string, isErr bool) {
	r.got = append(r.got, "result:"+n)
}

// MultiReporter 是三個入口共用的 fan-out（CLI／chatbot core／dashboard），先前 dashboard 另有一份
// 一模一樣的實作。合併後這裡是唯一版本：五個方法都必須送到【每一個】下游，漏一個就是某個介面
// 靜默少事件（辦公室看板或 SSE 串流會缺幀，而且不會有任何錯誤訊息）。
func TestMultiReporter_FansOutAllMethods(t *testing.T) {
	a, b := &recordingReporter{}, &recordingReporter{}
	m := MultiReporter{a, b}

	ctx := context.Background()
	m.OnThinking(ctx)
	m.OnTurn(ctx, 3)
	m.OnToolCall(ctx, "bash", "ls")
	m.OnToolResult(ctx, "bash", "ok", false)
	m.OnMessage(ctx, "done")

	want := []string{"think", "turn", "call:bash", "result:bash", "msg:done"}
	for i, r := range []*recordingReporter{a, b} {
		if !reflect.DeepEqual(r.got, want) {
			t.Errorf("下游 %d 收到 %v，期望 %v", i, r.got, want)
		}
	}
}

// 空 MultiReporter 不該 panic（呼叫端可能沒有任何投影目標）。
func TestMultiReporter_EmptyIsNoop(t *testing.T) {
	m := MultiReporter{}
	ctx := context.Background()
	m.OnThinking(ctx)
	m.OnTurn(ctx, 1)
	m.OnToolCall(ctx, "x", "")
	m.OnToolResult(ctx, "x", "", true)
	m.OnMessage(ctx, "")
}
