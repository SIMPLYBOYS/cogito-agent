// Package cmdutil 收斂各 cmd 入口的共用啟動樣板。歷史上多支 main 各自手抄「載入 .env + 初始化
// OTel」，多次漏接 InitTracing 導致 trace 不上報（claw-demo-mcp、claw-cli 都中過）。改由本套件
// 提供單一 Bootstrap，新入口照抄一行即可，結構上避免再漏。
package cmdutil

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/joho/godotenv"
)

// Bootstrap 載入 .env 並初始化 OTel 鏈路追蹤（設了 OTEL_EXPORTER_OTLP_ENDPOINT 才上報，否則 no-op）。
//
// 回傳的 flush 必須在程序退出前呼叫一次以送出緩衝的 span。建議：
//   - 一律 `defer flush()`（涵蓋正常返回）；
//   - 並在任何 log.Fatal / os.Exit【之前】再顯式呼叫一次（它們會略過 defer）。
//
// flush 內部以 sync.Once 去重，重複呼叫安全。
func Bootstrap(serviceName string) (flush func()) {
	_ = godotenv.Load()
	shutdown, err := observability.InitTracing(context.Background(), serviceName)
	if err != nil {
		log.Fatalf("初始化鏈路追蹤失敗: %v", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if e := shutdown(ctx); e != nil {
				log.Printf("[Tracing] flush 失敗: %v", e)
			}
		})
	}
}
