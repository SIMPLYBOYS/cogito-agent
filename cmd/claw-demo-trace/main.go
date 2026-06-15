// cmd/claw-demo-trace 是 ch19 的鏈路追蹤演示：觸發一個"一輪內並行調用兩個不同工具"的任務，
// 引擎在 .claw/traces/ 下產出一棵 Span 樹（Agent.Run → Turn-1 → 並行的兩個 Tool.Execute）。
// tracing 是引擎級的（在 engine.Run 內），所以其實任何入口都會產出 trace，這裡只是給個能觀察
// 併發樹形結構的場景。
package main

import (
	"context"
	"log"
	"os"

	"github.com/joho/godotenv"
	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/engine"
	"github.com/yourname/go-tiny-claw/internal/provider"
	"github.com/yourname/go-tiny-claw/internal/schema"
	"github.com/yourname/go-tiny-claw/internal/tools"
)

func main() {
	_ = godotenv.Load()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("請先在 .env 或環境變量中設置 ANTHROPIC_API_KEY")
	}

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

	log.Printf("\n>>> trace 文件已寫入: %s/.claw/traces/  （cat 出來可看到 Span 樹）\n", workDir)
}
