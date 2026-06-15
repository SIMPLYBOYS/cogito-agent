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
	// 讀取當前目錄的 .env（文件不存在也不報錯；不會覆蓋已存在的環境變量）
	_ = godotenv.Load()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("請先在 .env 或環境變量中設置 ANTHROPIC_API_KEY")
	}

	workDir, _ := os.Getwd()
	workDir += "/workspace" // 與 CLI 入口一致，agent 操作範圍隔離到 ./workspace；composer 也從此讀取 AGENTS.md / skills

	modelName := "claude-opus-4-8"
	llmProvider := provider.NewClaudeProvider(modelName)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// engine factory —— 每個會話現造一個掛了"該會話專屬 CostTracker"的引擎，實現
	// per-chat 成本記賬（registry/middleware 無狀態共享，tracker/session 按頻道隔離）。
	// EnableThinking=false（手動兩階段思考對 Claude 會退化成 <invoke> 文本）；
	// Slack 對話式入口默認不開 Plan Mode（否則每條消息都強制 PLAN.md/TODO.md 流程）。
	factory := func(sess *ctxpkg.Session) *engine.AgentEngine {
		tracked := observability.NewCostTracker(llmProvider, modelName, sess)
		return engine.NewAgentEngine(tracked, registry, false, false)
	}

	// workDir 同時用於：工具沙箱（上面註冊 tools 時）與各頻道 session 的 WorkDir
	bot := slackbot.NewSlackBot(factory, workDir)

	// 註冊高危操作審批 middleware。命中黑名單（如 bash rm -r / sudo / 覆蓋 .go）的工具調用
	// 會被掛起，把審批請求推回觸發它的 Slack 頻道（session.ID == channelID），
	// 等管理員回 "approve <taskID>" / "reject <taskID>" 才放行。
	registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
		args := string(call.Arguments)
		if !slackbot.IsDangerousCommand(call.Name, args) {
			return true, ""
		}
		channelID := ""
		if sess := engine.SessionFromContext(ctx); sess != nil {
			channelID = sess.ID
		}
		allowed, reason := slackbot.GlobalApprovalMgr.WaitForApproval(call.ID, channelID, call.Name, args, func(text string) {
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
	log.Printf("🚀 go-tiny-claw Slack 服務端已啟動，正在監聽 %s 端口\n", port)

	err := http.ListenAndServe(port, nil)
	if err != nil {
		log.Fatalf("服務器啟動失敗: %v", err)
	}
}
