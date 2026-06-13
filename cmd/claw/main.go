package main

import (
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/yourname/go-tiny-claw/internal/engine"
	"github.com/yourname/go-tiny-claw/internal/provider"
	"github.com/yourname/go-tiny-claw/internal/slackbot"
	"github.com/yourname/go-tiny-claw/internal/tools"
)

func main() {
	// 读取当前目录的 .env（文件不存在也不报错；不会覆盖已存在的环境变量）
	_ = godotenv.Load()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY")
	}

	workDir, _ := os.Getwd()
	workDir += "/workspace" // ch10: 与 CLI 入口一致，agent 操作范围隔离到 ./workspace；composer 也从此读取 AGENTS.md / skills

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// EnableThinking=false：手动两阶段"思考"(Phase 1 剥夺 tools) 会让 Claude 把工具调用
	// 退化成 <invoke> 文本而非结构化 tool_use，导致循环空转。Claude 用单阶段直接带 tools
	// 即可正常 ReAct；若要"思考"，应改用 claude.go 的原生 adaptive thinking，而非剥夺 tools。
	// Slack 是对话式入口，默认不开 Plan Mode（否则每条消息都会强制 PLAN.md/TODO.md 流程）
	eng := engine.NewAgentEngine(llmProvider, registry, false, false)

	// workDir 同时用于：工具沙箱（上面注册 tools 时）与各频道 session 的 WorkDir
	bot := slackbot.NewSlackBot(eng, workDir)

	http.HandleFunc("/webhook/event", bot.HandleEvent)

	port := ":48080"
	log.Printf("🚀 go-tiny-claw Slack 服务端已启动，正在监听 %s 端口\n", port)

	err := http.ListenAndServe(port, nil)
	if err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
