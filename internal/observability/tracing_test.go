package observability

import (
	"encoding/base64"
	"os"
	"testing"
)

func TestDeriveLangfuseAuthHeader(t *testing.T) {
	// 未手設 + 有金鑰 → 自動組正確的 Basic auth
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "")
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-lf-aaa")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk-lf-bbb")
	deriveLangfuseAuthHeader()
	want := "Authorization=Basic " + base64.StdEncoding.EncodeToString([]byte("pk-lf-aaa:sk-lf-bbb"))
	if got := os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"); got != want {
		t.Errorf("自動組 header 錯誤:\n got %q\nwant %q", got, want)
	}

	// 已手設 → 尊重，不覆蓋（避免搶掉使用者的自訂認證）
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Basic manual")
	deriveLangfuseAuthHeader()
	if got := os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"); got != "Authorization=Basic manual" {
		t.Errorf("已手設不應被覆蓋: got %q", got)
	}

	// 缺金鑰 → no-op（不留半套 header）
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "")
	t.Setenv("LANGFUSE_SECRET_KEY", "")
	deriveLangfuseAuthHeader()
	if got := os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"); got != "" {
		t.Errorf("缺金鑰不應設 header: got %q", got)
	}
}

// exporter 選擇的環境變數解析：純字串，決定追蹤是否啟用（Enabled 是 loop 昂貴序列化的閘）。
func TestExporterSelection(t *testing.T) {
	// 全部清空 → 未啟用
	t.Setenv("OTEL_TRACES_EXPORTER", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	if isConsoleExporter() || otlpEndpoint() != "" || Enabled() {
		t.Error("全未設時應未啟用任何 exporter")
	}

	// console/stdout 皆識別為 console exporter
	t.Setenv("OTEL_TRACES_EXPORTER", "Console") // 大小寫不敏感
	if !isConsoleExporter() || !Enabled() {
		t.Error("OTEL_TRACES_EXPORTER=Console 應識別為 console 且 Enabled")
	}
	t.Setenv("OTEL_TRACES_EXPORTER", "stdout")
	if !isConsoleExporter() {
		t.Error("stdout 也應識別為 console exporter")
	}

	// OTLP endpoint：主變數優先，否則退回 *_TRACES_ENDPOINT
	t.Setenv("OTEL_TRACES_EXPORTER", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel:4318")
	if otlpEndpoint() != "http://otel:4318" || !Enabled() {
		t.Error("設了 OTLP endpoint 應回該值且 Enabled")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://traces:4318")
	if otlpEndpoint() != "http://traces:4318" {
		t.Error("主變數未設時應退回 *_TRACES_ENDPOINT")
	}
}
