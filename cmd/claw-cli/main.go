// cmd/claw-cli 是命令行入口：一次性执行一个任务，事件经 TerminalReporter 打到 stdout。
// 与 cmd/claw（Slack 服务端）共用同一套 AgentEngine 与 Context Engineering 子系统。
//
// 用法：
//
//	go run ./cmd/claw-cli -prompt "帮我写一个 web server"        # 普通模式
//	go run ./cmd/claw-cli -plan -prompt "帮我写一个 web server"  # ch13 Plan Mode：状态外部化到 PLAN.md/TODO.md
package main

import (
	"context"
	"flag"
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
	promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述（留空则用内置默认任务）")
	planPtr := flag.Bool("plan", false, "开启 Plan Mode：状态外部化到 PLAN.md / TODO.md，支持断点续传 (ch13)")
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

	workDir, _ := os.Getwd()
	workDir += "/workspace" // agent 操作范围隔离到 ./workspace

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(llmProvider, registry, false, *planPtr)
	reporter := engine.NewTerminalReporter()

	// 固定 sessionID：同一 process 内多次调用共享短期记忆（进程重启则丢失——
	// 这正是 Plan Mode 的价值：内存丢了，但 PLAN.md/TODO.md 还在，可断点续传）。
	session := ctxpkg.GlobalSessionMgr.GetOrCreate("cli-session", workDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), session, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
