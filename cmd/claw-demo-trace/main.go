// cmd/claw-demo-trace 是鏈路追蹤演示：觸發一個"一輪內並行調用兩個不同工具"的任務，
// 引擎產出一棵 OTel Span 樹（Agent.Run → Turn-1 → 並行的兩個 Tool.Execute）。
// tracing 已改為 OTel SDK：設定 OTEL_EXPORTER_OTLP_ENDPOINT 後，span 會上報到 Jaeger/Langfuse
// 等後端，可在瀏覽器看到並發工具時間重疊的甘特圖；未設定則為 no-op（不輸出檔案）。
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("請先在 .env 或環境變量中設置 ANTHROPIC_API_KEY")
	}

	// 設了 OTEL_EXPORTER_OTLP_ENDPOINT 才會上報；否則為 no-op。
	shutdownTracing, err := observability.InitTracing(context.Background(), "go-tiny-claw-demo-trace")
	if err != nil {
		log.Fatalf("初始化鏈路追蹤失敗: %v", err)
	}
	defer func() {
		// 顯式 flush 並打印結果：BatchSpanProcessor 在退出時才送根 span，吞掉錯誤會讓「沒上報」變無聲。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdownTracing(ctx); err != nil {
			log.Printf("[Tracing] ❌ flush/關閉失敗（span 可能未送達）: %v", err)
		} else {
			log.Println("[Tracing] ✅ span 已 flush（若有設 OTLP 端點即已上報）")
		}
	}()

	workDir := "/tmp/claw_trace_demo"
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("創建演示目錄失敗: %v", err)
	}

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))

	eng := engine.NewAgentEngine(llmProvider, registry, false, false)
	reporter := engine.NewTerminalReporter()

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate("test_trace_001", workDir)

	// 觸發一個跨工具類型的併發任務，讓 trace 樹出現並行的兩個 Tool.Execute 子節點
	prompt := `
	為了加快執行速度，請你在一輪迴復中，【同時並行】完成以下兩件事：
	1. 使用 bash 工具執行 'sleep 2 && echo "系統環境檢查完畢"'
	2. 使用 write_file 工具，在當前目錄下創建一個 'trace_test.md'，內容寫上 "測試併發的寫入"。
	請確保你是分別調用兩個不同的工具，不要試圖把它們合併成一個命令！
	`

	log.Println("\n>>> 🚀 啟動帶 Tracing 鏈路追蹤的測試...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎崩潰: %v", err)
	}

	log.Println("\n>>> trace 已產出。設定 OTEL_EXPORTER_OTLP_ENDPOINT 指向 Jaeger/Langfuse，" +
		"即可在瀏覽器看到 Agent.Run → Turn-1 → 並行兩個 Tool.Execute 的甘特圖（時間重疊）。")
}
