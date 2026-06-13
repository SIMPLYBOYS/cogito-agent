// cmd/claw-demo-oom 是 ch12 的上下文压缩（OOM 防线）演示：用一个会读入巨型文件的
// 三步任务，触发 engine 内置的 Compactor，让"字符级压缩"行为眼见为凭。
// 自包含：启动时在 /tmp/claw_oom_demo 下生成一个巨大的 mock_log.txt。
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
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY")
	}

	// 自包含：准备工作区 + 一个远超压缩阈值的 mock_log.txt
	workDir := "/tmp/claw_oom_demo"
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("创建演示目录失败: %v", err)
	}
	line := "这是一段极其冗长的、无意义的服务器报错日志信息，用来模拟 OOM 场景\n"
	bigLog := strings.Repeat(line, 200) // ~200 行 ≈ 12KB，远超 3000 字符阈值
	if err := os.WriteFile(filepath.Join(workDir, "mock_log.txt"), []byte(bigLog), 0644); err != nil {
		log.Fatalf("写入 mock_log.txt 失败: %v", err)
	}

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))

	eng := engine.NewAgentEngine(llmProvider, registry, false, false)
	reporter := engine.NewTerminalReporter()

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate("test_oom_protection_001", workDir)

	prompt := `
	请帮我执行以下三个步骤：
	1. 使用 bash 执行 echo "开始排查日志"
	2. 读取当前目录下的巨大文件 mock_log.txt
	3. 用 bash 执行 date 命令获取当前时间，并告诉我任务完成。
	`
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
