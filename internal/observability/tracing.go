package observability

import (
	"context"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// InitTracing 依環境變數初始化全域 TracerProvider：
//   - 設了 OTEL_EXPORTER_OTLP_ENDPOINT（或 *_TRACES_ENDPOINT）→ 用 OTLP/HTTP exporter 上報。
//     端點可指向 OTel Collector / Jaeger / Langfuse 的 OTLP 入口；認證/標頭走標準
//     OTEL_EXPORTER_OTLP_HEADERS（如 Langfuse 的 Authorization: Basic <base64(pk:sk)>）。
//   - 未設 → 回傳 no-op shutdown，全域 tracer 維持 no-op（span 變零成本空操作，離線/測試不受影響）。
//
// 返回的 shutdown 應在程式結束時調用以 flush 緩衝的 span。
func InitTracing(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	}
	if endpoint == "" {
		log.Println("[Tracing] 未設定 OTEL_EXPORTER_OTLP_ENDPOINT，鏈路追蹤以 no-op 運行（不上報）。")
		return func(context.Context) error { return nil }, nil
	}

	// otlptracehttp 會自動讀取標準 OTEL_EXPORTER_OTLP_* 環境變數（endpoint / headers / insecure）。
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", serviceName)))
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	log.Printf("[Tracing] OTLP 鏈路追蹤已啟用，上報至 %s\n", endpoint)

	return tp.Shutdown, nil
}
