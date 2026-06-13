// cmd/claw-cli 是 ch10 的命令行入口：一次性执行一个任务，事件通过 TerminalReporter
// 实时打到 stdout。与 cmd/claw（Slack 服务端）共用同一套 AgentEngine 与 Context
// Engineering 子系统（PromptComposer + Skills + AGENTS.md），只是 Reporter 与入口不同。
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
	// 读取 .env（不存在也不报错；不覆盖已有环境变量）
	_ = godotenv.Load()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY")
	}

	workDir, _ := os.Getwd()
	workDir += "/workspace" // ch10: 将 agent 可操作范围隔离到 ./workspace 子目录

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// EnableThinking=false：手动两阶段"思考"(Phase 1 剥夺 tools) 会让 Claude 把工具调用
	// 退化成 <invoke> 文本而非结构化 tool_use，导致循环空转。Claude 用单阶段直接带 tools
	// 即可正常 ReAct；若要"思考"，应改用 claude.go 的原生 adaptive thinking，而非剥夺 tools。
	eng := engine.NewAgentEngine(llmProvider, registry, false)
	reporter := engine.NewTerminalReporter()

	// 默认任务来自 ch10；也支持通过命令行参数覆盖：
	//   go run ./cmd/claw-cli "你的自定义指令"
	prompt := `
	我需要在当前目录下新建一个 ping.go，提供一个简单的 http ping 接口。
	写完之后，帮我把代码用 git 提交一下。
	`
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	// ch11: 单 session 一次性运行。先把用户输入 Append 进 session，再 Run。
	session := ctxpkg.GlobalSessionMgr.GetOrCreate("cli-session", workDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), session, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
