package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// recTool 記錄自己是否被執行，並 sleep 製造可量測的物理耗時。
type recTool struct {
	mu  sync.Mutex
	ran bool
}

func (t *recTool) Name() string                      { return "rec" }
func (t *recTool) Definition() schema.ToolDefinition { return schema.ToolDefinition{Name: "rec"} }
func (t *recTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.mu.Lock()
	t.ran = true
	t.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	return "ok", nil
}
func (t *recTool) didRun() bool { t.mu.Lock(); defer t.mu.Unlock(); return t.ran }

// 計時中間件量到的是工具本身的物理執行耗時（不改 tool 源碼，純 Use 掛載）。
func TestTimingMiddleware_MeasuresToolExecution(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&recTool{})

	var gotName string
	var gotMs int64
	reg.Use(NewTimingMiddleware(func(n string, ms int64) { gotName, gotMs = n, ms }))

	res := reg.Execute(context.Background(), schema.ToolCall{ID: "1", Name: "rec", Arguments: []byte("{}")})
	if res.IsError {
		t.Fatalf("不應報錯: %s", res.Output)
	}
	if gotName != "rec" {
		t.Errorf("應記錄工具名 rec，got %q", gotName)
	}
	if gotMs < 15 {
		t.Errorf("應量到 ≥~20ms 的物理耗時，got %dms", gotMs)
	}
}

// 環繞順序 + 短路：外層 before/after 包住內層；內層不調 next 即短路，工具不執行。
func TestMiddleware_AroundOrderAndShortCircuit(t *testing.T) {
	reg := NewRegistry()
	tool := &recTool{}
	reg.Register(tool)

	var mu sync.Mutex
	var seq []string
	add := func(s string) { mu.Lock(); seq = append(seq, s); mu.Unlock() }

	reg.Use(func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
		add("outer-before")
		r := next(ctx, call)
		add("outer-after")
		return r
	})
	reg.Use(func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
		add("inner-shortcircuit")
		return schema.ToolResult{ToolCallID: call.ID, Output: "blocked", IsError: true} // 不調 next
	})

	res := reg.Execute(context.Background(), schema.ToolCall{ID: "1", Name: "rec", Arguments: []byte("{}")})
	if !res.IsError || res.Output != "blocked" {
		t.Errorf("內層短路應直接返回 blocked，got %+v", res)
	}
	if tool.didRun() {
		t.Error("短路後工具不應被執行")
	}
	if got := strings.Join(seq, ","); got != "outer-before,inner-shortcircuit,outer-after" {
		t.Errorf("環繞順序不對: %s", got)
	}
}
