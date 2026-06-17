package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// 驗證 StartSpan/AddAttribute/EndSpan 經 OTel SDK 正確產生 span：名稱、屬性、父子鏈路。
func TestSpan_RecordsViaOTel(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)

	ctx, root := StartSpan(context.Background(), "Agent.Run")
	root.AddAttribute("session.id", "chA")

	_, child := StartSpan(ctx, "Tool.Execute")
	child.AddAttribute("tool_name", "bash")
	child.AddAttribute("intercepted", true)
	child.EndSpan()
	root.EndSpan()

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("應記錄 2 個 span，got %d", len(spans))
	}

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range spans {
		byName[s.Name()] = s
	}
	rootSpan, okR := byName["Agent.Run"]
	toolSpan, okT := byName["Tool.Execute"]
	if !okR || !okT {
		t.Fatalf("span 名稱缺失: %v", byName)
	}

	// 父子鏈路：Tool.Execute 的 parent 應為 Agent.Run，且同屬一條 trace。
	if toolSpan.Parent().SpanID() != rootSpan.SpanContext().SpanID() {
		t.Error("Tool.Execute 的父應為 Agent.Run")
	}
	if toolSpan.SpanContext().TraceID() != rootSpan.SpanContext().TraceID() {
		t.Error("父子 span 應屬同一條 trace")
	}

	// 屬性映射：tool_name=bash（字串）、intercepted=true（布林）。
	var toolName string
	var intercepted bool
	for _, kv := range toolSpan.Attributes() {
		switch string(kv.Key) {
		case "tool_name":
			toolName = kv.Value.AsString()
		case "intercepted":
			intercepted = kv.Value.AsBool()
		}
	}
	if toolName != "bash" {
		t.Errorf("tool_name 屬性不對: %q", toolName)
	}
	if !intercepted {
		t.Error("intercepted 屬性應為 true")
	}
}

// 未設定全域 provider（no-op tracer）時，span 操作不應 panic（離線/未配置後端的退化路徑）。
func TestSpan_NoopWhenUnconfigured(t *testing.T) {
	otel.SetTracerProvider(otel.GetTracerProvider()) // 預設即 no-op
	_, s := StartSpan(context.Background(), "orphan")
	s.AddAttribute("k", "v")
	s.EndSpan()
}
