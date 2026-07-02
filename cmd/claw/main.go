package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
	"github.com/SIMPLYBOYS/cogito-agent/internal/cmdutil"
	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
	"github.com/SIMPLYBOYS/cogito-agent/internal/mcp"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/slackbot"
	"github.com/SIMPLYBOYS/cogito-agent/internal/telegrambot"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

func main() {
	// 載入 .env + 初始化 OTel（單一 bootstrap，避免漏接 InitTracing）。flush 在優雅關閉時呼叫。
	flush := cmdutil.Bootstrap("cogito-agent")

	// 選擇 LLM provider（COGITO_PROVIDER：claude 預設 / openai 相容）。
	llmProvider, modelName, errProv := provider.FromEnv()
	if errProv != nil {
		log.Fatal(errProv)
	}
	log.Printf("[provider] model=%s", modelName)

	// 載入並連接 MCP 伺服器（設了 COGITO_MCP_CONFIG 才啟用）：外部 MCP 工具會註冊進每個會話的
	// registry。連線是程式級長壽命的，優雅關閉時統一 Close。連不上的 server 略過、不影響啟動。
	var mcpClients []*mcp.Client
	var mcpGateway *mcp.Gateway
	if cfgPath := os.Getenv("COGITO_MCP_CONFIG"); cfgPath != "" {
		servers, errCfg := mcp.LoadConfig(cfgPath)
		if errCfg != nil {
			log.Fatalf("讀取 MCP 設定失敗: %v", errCfg)
		}
		for _, s := range servers {
			cl, errDial := mcp.Dial(context.Background(), s)
			if errDial != nil {
				log.Printf("[mcp] 連接 %q 失敗，略過: %v", s.Name, errDial)
				continue
			}
			mcpClients = append(mcpClients, cl)
			log.Printf("[mcp] 已連接 server %q", s.Name)
		}
		if len(mcpClients) > 0 {
			// 用 gateway 漸進式暴露：context 只帶輕量目錄 + 2 個 gateway 工具，而非 N 個完整 schema。
			if gw, errGw := mcp.NewGateway(context.Background(), mcpClients); errGw != nil {
				log.Printf("[mcp] 建立 gateway 失敗: %v", errGw)
			} else {
				mcpGateway = gw
				log.Printf("[mcp] gateway 就緒：%d 個外部工具經 mcp_call_tool 漸進式暴露", gw.Count())
			}
		}
	}

	rootDir, _ := os.Getwd()
	rootDir += "/workspace" // 工作區根目錄；各頻道隔離到其下 channels/<id> 子目錄（見 bot.channelWorkDir）

	// 沙箱執行器：COGITO_SANDBOX=docker 時 bash 命令丟進隔離容器（OS 級硬邊界），否則宿主機直跑。
	executor := sandbox.FromEnv()
	log.Printf("[sandbox] bash 執行模式: %s", sandbox.Describe(executor))

	// session 持久化：設 COGITO_SESSION_DIR 即把對話歷史/費用落地磁碟，重啟後按頻道 ID 復原。
	if store, dir := ctxpkg.StoreFromEnv(); store != nil {
		ctxpkg.GlobalSessionMgr.SetStore(store)
		log.Printf("[Session] 持久化已啟用: %s", dir)
	} else {
		log.Printf("[Session] 純記憶體模式（設 COGITO_SESSION_DIR 可跨重啟續傳）")
	}

	// 背景任務管理器：每會話一個（session 級作用域），統一收集供優雅關閉時 kill 掉所有殘留進程。
	var taskMgrs []*tools.TaskManager
	var taskMgrsMu sync.Mutex

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
		if chatbot.IsDangerousCommand(call.Name, args) {
			channelID := ""
			if s := engine.SessionFromContext(ctx); s != nil {
				channelID = s.ID
			}
			allowed, reason := chatbot.GlobalApprovalMgr.WaitForApproval(call.ID, channelID, call.Name, args, func(text string) {
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
		registry.Register(tools.NewBashToolWithExecutor(sess.WorkDir, executor))
		registry.Register(tools.NewEditFileTool(sess.WorkDir))
		registry.Register(tools.NewReadSkillTool(rootDir)) // 技能按需載入：與技能索引同源（根 workspace）
		registry.Register(tools.NewRecallTool(rootDir))    // 長期記憶按需檢索：與記憶索引同源（根 workspace）
		if selfEvolveEnabled() {                           // agent 可主動沉澱（與 post-task hook 互補；產物仍 gated）
			registry.Register(tools.NewConsolidateTool(llmProvider, rootDir, sess))
		}
		if mcpGateway != nil { // 外部 MCP 工具經 gateway 漸進式暴露（2 個工具 + 輕量目錄）
			for _, gt := range mcpGateway.Tools() {
				registry.Register(gt)
			}
		}
		// 背景任務工具（bash_background/task_output/task_kill/task_list）：每會話一個 TaskManager
		// （session 級作用域），rooted 在該會話 WorkDir、共用同一沙箱 executor。
		tm := tools.NewTaskManager(executor, sess.WorkDir)
		for _, tt := range tools.NewTaskTools(tm) {
			registry.Register(tt)
		}
		taskMgrsMu.Lock()
		taskMgrs = append(taskMgrs, tm)
		taskMgrsMu.Unlock()

		registry.Use(approval) // 外層：先審批（bash_background 也走同一危險黑名單）
		registry.Use(timing)   // 內層：只量工具本身執行耗時

		tracked := observability.NewCostTracker(llmProvider, modelName, sess)
		eng := engine.NewAgentEngine(tracked, registry, false, false)
		// 對話式入口預設開「滾動摘要 + history 有界化」（長對話跨逐出連貫 + 記憶體收斂）；
		// COGITO_SUMMARY=off 可關（bench/一次性任務走 NewAgentEngine 預設關，保持確定性）。
		eng.EnableSummary = os.Getenv("COGITO_SUMMARY") != "off"
		// per-channel Plan Mode：由該頻道 session 的切換狀態決定（`plan on`/`plan off`）；預設關，
		// 短任務/閒聊免計畫檔儀式，長任務手動開即啟用目標錨 + 確定性步驟跳過。
		eng.PlanMode = sess.PlanMode()
		// 執行期讀【已套用】的調參（.claw/config.json，由 apply config 從提案晉升）——閉合參數自調飛輪。
		if k, ok := evolve.LoadKnobs(rootDir); ok {
			if k.MaxTurns > 0 {
				eng.MaxTurns = k.MaxTurns
			}
			if k.MaxConcurrentTools > 0 {
				eng.MaxConcurrentTools = k.MaxConcurrentTools
			}
			if k.MaxCostUSD > 0 {
				eng.MaxCostUSD = k.MaxCostUSD
			}
		}
		// 技能（.claw/skills）與 AGENTS.md 從【共享根目錄】讀，與 per-channel 工作產物分離：
		// 工具 rooted 在 sess.WorkDir（各頻道子目錄），但配置/技能是全 bot 共用資產。
		eng.AssetsDir = rootDir

		// 子智能體只讀能力沙箱：只給 read_file + bash（探索/find/grep 用），無 write/edit、
		// 無 spawn_subagent（杜絕遞迴）；同樣掛審批，子 agent 的危險 bash 也要人工放行。
		// 註冊 spawn_subagent 後，主 Agent 一次吐多個即可並行委派多路偵察兵（引擎並行 fan-out）。
		readOnly := tools.NewRegistry()
		readOnly.Register(tools.NewReadFileTool(sess.WorkDir))
		readOnly.Register(tools.NewBashToolWithExecutor(sess.WorkDir, executor))
		readOnly.Use(approval)
		readOnly.Use(timing)
		// 把本請求的 reporter 接進子智能體：子 agent 的逐步進度（RunSub 內以「[Subagent] …」
		// 前綴回報）也會串流回本頻道。SlackReporter 的 PostMessage 對並發安全，多隊偵察兵
		// 同時回報只是訊息交錯。
		registry.Register(tools.NewSubagentTool(eng, readOnly, reporter, rootDir)) // skillsBaseDir=rootDir：可綁定技能進子 context

		return eng
	}

	bot = slackbot.NewSlackBot(factory, rootDir)

	// Tier 4 自我進化（opt-in）：任務成功後反思軌跡。安全鐵律一致——產物只進【暫存區】、不自動生效，
	// 須人工 review（技能用 skillgate 晉升；提案記憶 apply 後放行為 .claw/memory/ 記錄才生效）。
	var skillSynth *evolve.SkillSynthesizer
	var memSynth *evolve.MemorySynthesizer
	if os.Getenv("COGITO_SKILL_SYNTH") == "1" {
		skillSynth = evolve.NewSkillSynthesizer(llmProvider, filepath.Join(rootDir, ".claw", evolve.ProposedSkillsDirName))
		log.Printf("[evolve] 技能自生成已啟用（寫入 .claw/%s，需人工 review）", evolve.ProposedSkillsDirName)
	}
	if os.Getenv("COGITO_MEMORY_SYNTH") == "1" {
		memSynth = evolve.NewMemorySynthesizer(llmProvider, rootDir)
		log.Printf("[evolve] 記憶自更新已啟用（寫入 .claw/%s，apply 後放行為長期記憶記錄）", evolve.ProposedMemoryFileName)
	}
	var kgExtract *evolve.RelationExtractor
	if os.Getenv("COGITO_KG_SYNTH") == "1" {
		kgExtract = evolve.NewRelationExtractor(llmProvider, rootDir)
		log.Printf("[evolve] KG 關係抽取已啟用（任務後抽 typed 關係 → .claw/kg/edges.proposed.jsonl，需 apply-edges 過 gate；每次任務多一次 LLM 呼叫）")
	}
	// 自我進化的鉤子做成共用變數（與平台無關，用 chatbot.SendMessage 按 session.ID 路由回對的平台），
	// 同一套同時掛給 Slack 與 Telegram，行為一致。未啟用任一 synth 時為 nil（核心會略過）。
	var postRun chatbot.PostRunHook
	var postFailure chatbot.PostFailureHook
	if skillSynth != nil || memSynth != nil || kgExtract != nil {
		postRun = func(ctx context.Context, session *ctxpkg.Session, taskPrompt string) {
			history := session.GetWorkingMemory(0)
			if skillSynth != nil {
				if path, err := skillSynth.Reflect(ctx, taskPrompt, history); err != nil {
					log.Printf("[evolve] 技能反思失敗（不影響任務）: %v", err)
				} else if path != "" {
					log.Printf("[evolve] 💡 提案技能：%s", path)
					chatbot.SendMessage(session.ID, fmt.Sprintf("💡 我從這次任務萃取了一個*提案技能* `%s`，已存到暫存區，需你 review 後手動啟用（不會自動生效）。", filepath.Base(path)))
				}
			}
			if memSynth != nil {
				if added, err := memSynth.Reflect(ctx, taskPrompt, history); err != nil {
					log.Printf("[evolve] 記憶反思失敗（不影響任務）: %v", err)
				} else if len(added) > 0 {
					log.Printf("[evolve] 🧠 新增 %d 條提案記憶", len(added))
					chatbot.SendMessage(session.ID, memoryProposalMsg("慣例", added))
				}
			}
			if kgExtract != nil {
				if n, err := kgExtract.Extract(ctx); err != nil {
					log.Printf("[evolve] KG 關係抽取失敗（不影響任務）: %v", err)
				} else if n > 0 {
					log.Printf("[evolve] 🔗 新增 %d 條提案關係", n)
					chatbot.SendMessage(session.ID, fmt.Sprintf("🔗 我從記憶中抽出 *%d 條提案關係*（尚未生效）。回覆 `apply edges` 過 gate 併入知識圖譜，或 `reject edges` 丟棄。", n))
				}
			}
		}
		// live Reflexion：失敗的真實互動 → 萃取教訓進提案記憶（與成功路徑互補；同樣須人工併入）。
		if memSynth != nil {
			postFailure = func(ctx context.Context, session *ctxpkg.Session, taskPrompt, failureMsg string) {
				if added, err := memSynth.ReflectFailure(ctx, taskPrompt, session.GetWorkingMemory(0), failureMsg); err != nil {
					log.Printf("[evolve] 失敗反思失敗（不影響任務）: %v", err)
				} else if len(added) > 0 {
					log.Printf("[evolve] 🧠 從失敗萃取 %d 條教訓", len(added))
					chatbot.SendMessage(session.ID, memoryProposalMsg("失敗教訓", added))
				}
			}
		}
	}
	bot.SetPostRunHook(postRun)
	bot.SetPostFailureHook(postFailure)

	// 監聽 SIGINT/SIGTERM 以優雅關閉：先停傳輸層（websocket/長輪詢隨 ctx 取消），再 flush OTel span。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 多平台（opt-in）：設了 TELEGRAM_BOT_TOKEN 就同時開 Telegram 長輪詢，與 Slack 同進程、共用
	// 同一 factory 與自我進化鉤子；會話/工作目錄靠 platform 前綴命名空間天然隔離（slack: vs telegram:）。
	if os.Getenv("TELEGRAM_BOT_TOKEN") != "" {
		tg := telegrambot.NewTelegramBot(factory, rootDir)
		tg.SetPostRunHook(postRun)
		tg.SetPostFailureHook(postFailure)
		go tg.Start(ctx)
		tg.ResumeInterrupted() // 跨重啟續跑：續本次被硬砍中斷的 Telegram 任務（需 AUTO_RESUME + SESSION_DIR）
	}

	// Slack 走 Socket Mode（outbound websocket，免公開 URL）。兩平台都不需要對外端口，零基建。
	go bot.Start(ctx)
	bot.ResumeInterrupted() // 跨重啟續跑：續上次被硬砍中斷的 Slack 任務（需 AUTO_RESUME + SESSION_DIR）

	<-ctx.Done()
	log.Println("收到關閉信號，優雅關閉中...")
	stop()  // 取消 ctx → Slack websocket / Telegram 長輪詢各自收線
	flush() // flush OTel span（內部自帶 timeout + once 去重）
	for _, cl := range mcpClients {
		_ = cl.Close() // 結束 MCP 伺服器子進程，避免殘留
	}
	taskMgrsMu.Lock()
	for _, tm := range taskMgrs {
		tm.KillAll() // 收掉所有背景任務，避免殘留孤兒進程
	}
	taskMgrsMu.Unlock()
	if c, ok := executor.(interface{ Close() error }); ok {
		_ = c.Close() // 移除 per-session sandbox 容器（docker 模式）
	}
}

// memoryProposalMsg 組裝「提案記憶」通知：直接列出內容 + 一鍵 apply/reject 指令（閘在 Slack 內，免去 cat 檔案）。
func memoryProposalMsg(kind string, added []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🧠 我從這次任務學到 %d 條*提案%s*（尚未生效）：\n", len(added), kind)
	for _, l := range added {
		b.WriteString("• " + l + "\n")
	}
	b.WriteString("回覆 `apply memory` 放行為可檢索的長期記憶（存成記憶節點、recall 取用），或 `reject memory` 丟棄。")
	return b.String()
}

// selfEvolveEnabled 回報是否啟用了任一自我進化開關——決定要不要把 consolidate 工具暴露給 agent。
func selfEvolveEnabled() bool {
	return os.Getenv("COGITO_SKILL_SYNTH") == "1" ||
		os.Getenv("COGITO_MEMORY_SYNTH") == "1" ||
		os.Getenv("COGITO_KG_SYNTH") == "1"
}
