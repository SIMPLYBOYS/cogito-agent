// cmd/claw-demo-oom 是上下文壓縮（OOM 防線）演示：用一個會讀入巨型文件的
// 三步任務，觸發 engine 內置的 Compactor，讓"字符級壓縮"行為眼見為憑。
// 自包含：啟動時在 /tmp/claw_oom_demo 下生成一個巨大的 mock_log.txt。
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

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

	// 自包含：準備工作區 + 一個遠超壓縮閾值的 mock_log.txt
	workDir := "/tmp/claw_oom_demo"
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("創建演示目錄失敗: %v", err)
	}
	line := "這是一段極其冗長的、無意義的服務器報錯日誌信息，用來模擬 OOM 場景\n"
	bigLog := strings.Repeat(line, 200) // ~200 行 ≈ 12KB，遠超 3000 字符閾值
	if err := os.WriteFile(filepath.Join(workDir, "mock_log.txt"), []byte(bigLog), 0644); err != nil {
		log.Fatalf("寫入 mock_log.txt 失敗: %v", err)
	}

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))

	eng := engine.NewAgentEngine(llmProvider, registry, false, false)
	reporter := engine.NewTerminalReporter()

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate("test_oom_protection_001", workDir)

	prompt := `
	請幫我執行以下三個步驟：
	1. 使用 bash 執行 echo "開始排查日誌"
	2. 讀取當前目錄下的巨大文件 mock_log.txt
	3. 用 bash 執行 date 命令獲取當前時間，並告訴我任務完成。
	`
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎運行崩潰: %v", err)
	}
}
