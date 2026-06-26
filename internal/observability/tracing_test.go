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
