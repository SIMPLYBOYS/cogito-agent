package main

import (
	"context"
	"log"
	"net/http"
	"os"

	ctxpkg "github.com/SIMPLYBOYS/go-tiny-claw/internal/context"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/engine"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/observability"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/provider"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/slackbot"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/tools"
	"github.com/joho/godotenv"
)

func main() {
	// 讀取當前目錄的 .env（文件不存在也不報錯；不會覆蓋已存在的環境變量）
	_ = godotenv.Load()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("請先在 .env 或環境變量中設置 ANTHROPIC_API_KEY")
	}

	rootDir, _ := os.Getwd()
	rootDir += "/workspace" // 工作區根目錄；各頻道隔離到其下 channels/<id> 子目錄（見 bot.channelWorkDir）

	modelName := "claude-opus-4-8"
	llmProvider := provider.NewClaudeProvider(modelName)

	// bot 先聲明後賦值：factory/中介層的閉包按引用捕獲 bot，工廠在服務啟動後才被調用，屆時已賦值。
	var bot *slackbot.SlackBot

	// engine factory —— 每個會話（頻道）現造引擎：工具 rooted 在【該會話自己的 WorkDir】
	// （per-channel 磁碟隔離的關鍵——不再全局共用一個 registry），並掛上專屬 CostTracker 與
	// 審批 middleware。EnableThinking=false（手動兩階段思考對 Claude 會退化成 <invoke> 文本）；
	// Slack 對話式入口默認不開 Plan Mode。
	factory := func(sess *ctxpkg.Session) *engine.AgentEngine {
		registry := tools.NewRegistry()
		registry.Register(tools.NewReadFileTool(sess.WorkDir))
		registry.Register(tools.NewWriteFileTool(sess.WorkDir))
		registry.Register(tools.NewBashTool(sess.WorkDir))
		registry.Register(tools.NewEditFileTool(sess.WorkDir))

		// 高危操作審批 middleware：命中黑名單（如 bash rm -r / sudo / 覆蓋 .go）的工具調用
		// 會被掛起，把審批請求推回觸發它的 Slack 頻道（session.ID == channelID），
		// 等管理員回 "approve" / "reject"（或帶 taskID）才放行。
		registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
			args := string(call.Arguments)
			if !slackbot.IsDangerousCommand(call.Name, args) {
				return true, ""
			}
			channelID := ""
			if s := engine.SessionFromContext(ctx); s != nil {
				channelID = s.ID
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

		tracked := observability.NewCostTracker(llmProvider, modelName, sess)
		return engine.NewAgentEngine(tracked, registry, false, false)
	}

	bot = slackbot.NewSlackBot(factory, rootDir)

	http.HandleFunc("/webhook/event", bot.HandleEvent)

	port := ":48080"
	log.Printf("🚀 go-tiny-claw Slack 服務端已啟動，正在監聽 %s 端口\n", port)

	err := http.ListenAndServe(port, nil)
	if err != nil {
		log.Fatalf("服務器啟動失敗: %v", err)
	}
}
