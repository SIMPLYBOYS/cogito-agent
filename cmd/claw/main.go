package main

import (
	"context"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/yourname/go-tiny-claw/internal/engine"
	"github.com/yourname/go-tiny-claw/internal/provider"
	"github.com/yourname/go-tiny-claw/internal/tools"
)

func main() {
	// 读取当前目录的 .env（文件不存在也不报错；不会覆盖已存在的环境变量）
	_ = godotenv.Load()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY")
	}

	workDir, _ := os.Getwd()

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()

	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir)) // 挂载 Edit 工具

	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	prompt := `
	我当前目录下有一个 server.go 文件。
	请帮我把里面 "TODO: 增加鉴权逻辑" 下面的那个 if 语句，整个替换为：
	if user == nil {
		fmt.Println("Forbidden!")
		return
	}
	`

	err := eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
