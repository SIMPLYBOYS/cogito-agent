// cmd/claw-cli 是升級後的生產級命令行入口：把前面累積的全部能力組裝成一個通用 CLI。
// 事件經 TerminalReporter 實時打到 stdout；provider 外掛 CostTracker 自動記賬（trace 由
// engine.Run 內部導出到 <dir>/.claw/traces/）；結束打印花費 + token 報表。
//
// 用法：
//
//	go run ./cmd/claw-cli -prompt "幫我寫一個 web server"
//	go run ./cmd/claw-cli -prompt "繼續上次的任務" -dir ./myproj -session task_001   # 指定工作區 + 斷點續傳
//	go run ./cmd/claw-cli -plan -prompt "..."                                       # 開 Plan Mode
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/engine"
	"github.com/yourname/go-tiny-claw/internal/observability"
	"github.com/yourname/go-tiny-claw/internal/provider"
	"github.com/yourname/go-tiny-claw/internal/schema"
	"github.com/yourname/go-tiny-claw/internal/tools"
)

func main() {
	promptPtr := flag.String("prompt", "", "要交給 Agent 執行的任務描述（留空則用內置默認任務）")
	// 默認 ./workspace 而非書本的 "."：保持工作區沙箱、避免汙染本倉庫。需要時可顯式指定任意目錄。
	dirPtr := flag.String("dir", "./workspace", "Agent 工作區目錄")
	sessionPtr := flag.String("session", "cli-session", "會話 ID，支持斷點續傳")
	planPtr := flag.Bool("plan", false, "開啟 Plan Mode：狀態外部化到 PLAN.md / TODO.md")
	flag.Parse()

	_ = godotenv.Load()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("請先在 .env 或環境變量中設置 ANTHROPIC_API_KEY")
	}

	prompt := *promptPtr
	if prompt == "" {
		// 內置默認任務
		prompt = `
	我需要在當前目錄下新建一個 ping.go，提供一個簡單的 http ping 接口。
	寫完之後，幫我把代碼用 git 提交一下。
	`
	}

	workDir, err := filepath.Abs(*dirPtr)
	if err != nil {
		log.Fatalf("解析工作區路徑失敗: %v", err)
	}

	fmt.Println("==================================================")
	fmt.Printf("🚀 go-tiny-claw CLI | 📁 工作區: %s\n", workDir)
	fmt.Println("==================================================")

	modelName := "claude-opus-4-8"
	realProvider := provider.NewClaudeProvider(modelName)

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(*sessionPtr, workDir)

	// 用 CostTracker 包裹 provider 自動記賬；trace 由 engine.Run 內部自動導出
	trackedProvider := observability.NewCostTracker(realProvider, modelName, sess)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(trackedProvider, registry, false, *planPtr)
	reporter := engine.NewTerminalReporter()

	fmt.Printf("\n🎯 收到任務: %s\n\n", prompt)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("\n💥 引擎運行崩潰: %v", err)
	}

	fmt.Println("\n==================================================")
	fmt.Printf("💰 Session 累計消耗: $%.6f | Token: Input %d, Output %d\n",
		sess.TotalCostUSD, sess.TotalPromptTokens, sess.TotalCompletionTokens)
	fmt.Println("==================================================")
}
