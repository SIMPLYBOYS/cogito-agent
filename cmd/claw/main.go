package main

import (
	"context"
	"fmt"
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
	// 高危操作審批 middleware（環繞式）：命中黑名單（如 bash rm -r / sudo / 覆蓋 .go）的工具調用
	// 會被掛起，把審批請求推回觸發它的 Slack 頻道（session.ID == channelID），等管理員回
	// approve/reject 才放行（不調 next 即短路）。抽成函式以便主工具池與子智能體只讀池共用
	//（子 agent 的 bash 同樣要過審批，不留後門）。
	approval := func(ctx context.Context, call schema.ToolCall, next tools.ToolHandler) schema.ToolResult {
		args := string(call.Arguments)
		if slackbot.IsDangerousCommand(call.Name, args) {
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
				return schema.ToolResult{ToolCallID: call.ID, Output: fmt.Sprintf("執行被系統攔截。原因: %s", reason), IsError: true}
			}
		}
		return next(ctx, call)
	}

	// 計時 middleware：記錄工具的物理執行耗時（如一個編譯 5 分鐘的 bash）。掛在 approval【之後】，
	// 故只量工具本身、不含人工審批等待。捕獲不修改 bash.go 等任何工具源碼（裝飾器攔截）。
	timing := tools.NewTimingMiddleware(func(toolName string, durationMs int64) {
		log.Printf("[Timing] 工具 %s 物理執行耗時 %dms\n", toolName, durationMs)
	})

	factory := func(sess *ctxpkg.Session, reporter engine.Reporter) *engine.AgentEngine {
		registry := tools.NewRegistry()
		registry.Register(tools.NewReadFileTool(sess.WorkDir))
		registry.Register(tools.NewWriteFileTool(sess.WorkDir))
		registry.Register(tools.NewBashTool(sess.WorkDir))
		registry.Register(tools.NewEditFileTool(sess.WorkDir))
		registry.Use(approval) // 外層：先審批
		registry.Use(timing)   // 內層：只量工具本身執行耗時

		tracked := observability.NewCostTracker(llmProvider, modelName, sess)
		eng := engine.NewAgentEngine(tracked, registry, false, false)
		// 技能（.claw/skills）與 AGENTS.md 從【共享根目錄】讀，與 per-channel 工作產物分離：
		// 工具 rooted 在 sess.WorkDir（各頻道子目錄），但配置/技能是全 bot 共用資產。
		eng.AssetsDir = rootDir

		// 子智能體只讀能力沙箱：只給 read_file + bash（探索/find/grep 用），無 write/edit、
		// 無 spawn_subagent（杜絕遞迴）；同樣掛審批，子 agent 的危險 bash 也要人工放行。
		// 註冊 spawn_subagent 後，主 Agent 一次吐多個即可並行委派多路偵察兵（引擎並行 fan-out）。
		readOnly := tools.NewRegistry()
		readOnly.Register(tools.NewReadFileTool(sess.WorkDir))
		readOnly.Register(tools.NewBashTool(sess.WorkDir))
		readOnly.Use(approval)
		readOnly.Use(timing)
		// 把本請求的 reporter 接進子智能體：子 agent 的逐步進度（RunSub 內以「[Subagent] …」
		// 前綴回報）也會串流回本頻道。SlackReporter 的 PostMessage 對並發安全，多隊偵察兵
		// 同時回報只是訊息交錯。
		registry.Register(tools.NewSubagentTool(eng, readOnly, reporter))

		return eng
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
