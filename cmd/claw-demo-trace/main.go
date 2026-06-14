// cmd/claw-demo-trace 是 ch19 的链路追踪演示：触发一个"一轮内并行调用两个不同工具"的任务，
// 引擎在 .claw/traces/ 下产出一棵 Span 树（Agent.Run → Turn-1 → 并行的两个 Tool.Execute）。
// tracing 是引擎级的（在 engine.Run 内），所以其实任何入口都会产出 trace，这里只是给个能观察
// 并发树形结构的场景。
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
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY")
	}

	workDir := "/tmp/claw_trace_demo"
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("创建演示目录失败: %v", err)
	}

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))

	eng := engine.NewAgentEngine(llmProvider, registry, false, false)
	reporter := engine.NewTerminalReporter()

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate("test_trace_001", workDir)

	// 触发一个跨工具类型的并发任务，让 trace 树出现并行的两个 Tool.Execute 子节点
	prompt := `
	为了加快执行速度，请你在一轮回复中，【同时并行】完成以下两件事：
	1. 使用 bash 工具执行 'sleep 2 && echo "系统环境检查完毕"'
	2. 使用 write_file 工具，在当前目录下创建一个 'trace_test.md'，内容写上 "测试并发的写入"。
	请确保你是分别调用两个不同的工具，不要试图把它们合并成一个命令！
	`

	log.Println("\n>>> 🚀 启动带 Tracing 链路追踪的测试...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎崩溃: %v", err)
	}

	log.Printf("\n>>> trace 文件已写入: %s/.claw/traces/  （cat 出来可看到 Span 树）\n", workDir)
}
