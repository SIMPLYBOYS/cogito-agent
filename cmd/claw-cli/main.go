// cmd/claw-cli 是 ch21 升级后的生产级命令行入口：把前面累积的全部能力组装成一个通用 CLI。
// 事件经 TerminalReporter 实时打到 stdout；provider 外挂 ch18 CostTracker 自动记账（trace 由
// engine.Run 内部导出到 <dir>/.claw/traces/）；结束打印花费 + token 报表。
//
// 用法：
//
//	go run ./cmd/claw-cli -prompt "帮我写一个 web server"
//	go run ./cmd/claw-cli -prompt "继续上次的任务" -dir ./myproj -session task_001   # 指定工作区 + 断点续传
//	go run ./cmd/claw-cli -plan -prompt "..."                                       # 开 Plan Mode (ch13)
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
	promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述（留空则用内置默认任务）")
	// 默认 ./workspace 而非书本的 "."：保持工作区沙箱、避免污染本仓库。需要时可显式指定任意目录。
	dirPtr := flag.String("dir", "./workspace", "Agent 工作区目录")
	sessionPtr := flag.String("session", "cli-session", "会话 ID，支持断点续传")
	planPtr := flag.Bool("plan", false, "开启 Plan Mode：状态外部化到 PLAN.md / TODO.md (ch13)")
	flag.Parse()

	_ = godotenv.Load()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY")
	}

	prompt := *promptPtr
	if prompt == "" {
		// 内置默认任务（ch10）
		prompt = `
	我需要在当前目录下新建一个 ping.go，提供一个简单的 http ping 接口。
	写完之后，帮我把代码用 git 提交一下。
	`
	}

	workDir, err := filepath.Abs(*dirPtr)
	if err != nil {
		log.Fatalf("解析工作区路径失败: %v", err)
	}

	fmt.Println("==================================================")
	fmt.Printf("🚀 go-tiny-claw CLI | 📁 工作区: %s\n", workDir)
	fmt.Println("==================================================")

	modelName := "claude-opus-4-8"
	realProvider := provider.NewClaudeProvider(modelName)

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(*sessionPtr, workDir)

	// ch18: 用 CostTracker 包裹 provider 自动记账；ch19 的 trace 由 engine.Run 内部自动导出
	trackedProvider := observability.NewCostTracker(realProvider, modelName, sess)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(trackedProvider, registry, false, *planPtr)
	reporter := engine.NewTerminalReporter()

	fmt.Printf("\n🎯 收到任务: %s\n\n", prompt)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("\n💥 引擎运行崩溃: %v", err)
	}

	fmt.Println("\n==================================================")
	fmt.Printf("💰 Session 累计消耗: $%.6f | Token: Input %d, Output %d\n",
		sess.TotalCostUSD, sess.TotalPromptTokens, sess.TotalCompletionTokens)
	fmt.Println("==================================================")
}
