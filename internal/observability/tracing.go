package observability

import (
	"context"
	"log"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// InitTracing 依環境變數初始化全域 TracerProvider，exporter 選擇順序：
//   - OTEL_TRACES_EXPORTER=console|stdout → stdout exporter（不接後端也能本地看 span，便於開發）。
//   - OTEL_EXPORTER_OTLP_ENDPOINT（或 *_TRACES_ENDPOINT）→ OTLP/HTTP 上報。端點可指向
//     OTel Collector / Jaeger / Langfuse 的 OTLP 入口；認證走標準 OTEL_EXPORTER_OTLP_HEADERS。
//   - 兩者皆未設 → no-op（span 零成本空操作，離線/測試不受影響）。
//
// 返回的 shutdown 應在程式結束時調用以 flush 緩衝的 span。
func InitTracing(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	var (
		exp  sdktrace.SpanExporter
		err  error
		mode string
	)

	switch {
	case isConsoleExporter():
		exp, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
		mode = "stdout(console)"
	case otlpEndpoint() != "":
		// otlptracehttp 自動讀取標準 OTEL_EXPORTER_OTLP_* 環境變數（endpoint / headers / insecure）。
		exp, err = otlptracehttp.New(ctx)
		mode = "OTLP → " + otlpEndpoint()
	default:
		log.Println("[Tracing] 未設定 exporter（OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_TRACES_EXPORTER），鏈路追蹤以 no-op 運行。")
		return func(context.Context) error { return nil }, nil
	}
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
	log.Printf("[Tracing] 鏈路追蹤已啟用（%s）\n", mode)

	return tp.Shutdown, nil
}

func otlpEndpoint() string {
	if e := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); e != "" {
		return e
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
}

func isConsoleExporter() bool {
	v := strings.ToLower(os.Getenv("OTEL_TRACES_EXPORTER"))
	return v == "console" || v == "stdout"
}
