package observability

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/SIMPLYBOYS/cogito-agent"

// Span 是對 OTel trace.Span 的薄包裝：保留專案既有的 StartSpan/EndSpan/AddAttribute API，
// 底層改由 OTel SDK 負責 TraceID/SpanID 分配、context 父子傳播、批次與匯出
// （OTLP → Jaeger / Langfuse / 任何相容後端）。未配置後端時全域 tracer 為 no-op，span 變零成本空操作。
type Span struct {
	otel oteltrace.Span
}

// StartSpan 開啟一個新跨度，並把它放回衍生 ctx（OTel 以 context 傳播父子關係，與舊實現一致）。
func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	ctx, s := otel.Tracer(tracerName).Start(ctx, name)
	return ctx, &Span{otel: s}
}

// EndSpan 結束跨度（耗時由 OTel 依 start/end 自動計算）。
func (s *Span) EndSpan() {
	if s != nil && s.otel != nil {
		s.otel.End()
	}
}

// AddAttribute 為當前 Span 記錄一條元數據（映射為 OTel attribute）。
func (s *Span) AddAttribute(key string, value interface{}) {
	if s == nil || s.otel == nil {
		return
	}
	s.otel.SetAttributes(toAttr(key, value))
}

func toAttr(key string, value interface{}) attribute.KeyValue {
	switch v := value.(type) {
	case string:
		// 修：按位元組截斷的字串（如工具輸出含中文時 s[:100] 切到多位元組字元中間）會是非法 UTF-8，
		// 讓 OTLP 匯出整批失敗。在設屬性的單一出口收斂成合法 UTF-8，無論上游怎麼截。
		return attribute.String(key, strings.ToValidUTF8(v, ""))
	case bool:
		return attribute.Bool(key, v)
	case int:
		return attribute.Int(key, v)
	case int64:
		return attribute.Int64(key, v)
	case float64:
		return attribute.Float64(key, v)
	default:
		return attribute.String(key, strings.ToValidUTF8(fmt.Sprintf("%v", v), ""))
	}
}
