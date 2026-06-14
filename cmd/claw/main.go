package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/engine"
	"github.com/yourname/go-tiny-claw/internal/observability"
	"github.com/yourname/go-tiny-claw/internal/provider"
	"github.com/yourname/go-tiny-claw/internal/schema"
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

	modelName := "claude-opus-4-8"
	llmProvider := provider.NewClaudeProvider(modelName)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// ch22: engine factory —— 每个会话现造一个挂了"该会话专属 CostTracker"的引擎，实现
	// per-chat 成本记账（registry/middleware 无状态共享，tracker/session 按频道隔离）。
	// EnableThinking=false（手动两阶段思考对 Claude 会退化成 <invoke> 文本）；
	// Slack 对话式入口默认不开 Plan Mode（否则每条消息都强制 PLAN.md/TODO.md 流程）。
	factory := func(sess *ctxpkg.Session) *engine.AgentEngine {
		tracked := observability.NewCostTracker(llmProvider, modelName, sess)
		return engine.NewAgentEngine(tracked, registry, false, false)
	}

	// workDir 同时用于：工具沙箱（上面注册 tools 时）与各频道 session 的 WorkDir
	bot := slackbot.NewSlackBot(factory, workDir)

	// ch16: 注册高危操作审批 middleware。命中黑名单（如 bash rm -r / sudo / 覆盖 .go）的工具调用
	// 会被挂起，把审批请求推回触发它的 Slack 频道（session.ID == channelID），
	// 等管理员回 "approve <taskID>" / "reject <taskID>" 才放行。
	registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
		args := string(call.Arguments)
		if !slackbot.IsDangerousCommand(call.Name, args) {
			return true, ""
		}
		channelID := ""
		if sess := engine.SessionFromContext(ctx); sess != nil {
			channelID = sess.ID
		}
		allowed, reason := slackbot.GlobalApprovalMgr.WaitForApproval(call.ID, call.Name, args, func(text string) {
			if channelID != "" {
				bot.SendMessage(channelID, text)
			}
		})
		if !allowed {
			return false, reason
		}
		return true, ""
	})

	http.HandleFunc("/webhook/event", bot.HandleEvent)

	port := ":48080"
	log.Printf("🚀 go-tiny-claw Slack 服务端已启动，正在监听 %s 端口\n", port)

	err := http.ListenAndServe(port, nil)
	if err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
