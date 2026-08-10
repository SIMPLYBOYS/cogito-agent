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
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/agentkit"
	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
	"github.com/SIMPLYBOYS/cogito-agent/internal/cmdutil"
	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/cron"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/policy"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/slackbot"
	"github.com/SIMPLYBOYS/cogito-agent/internal/telegrambot"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// mcpDialTimeout 是啟動時連接單一 MCP server（含 initialize 握手 / tools/list）的上限。
// 連不上就略過該 server，不拖垮整個 bot 啟動。
const mcpDialTimeout = 30 * time.Second

func main() {
	cmdutil.PrintBanner() // 啟動 logo（非終端自動不印）
	// 載入 .env + 初始化 OTel（單一 bootstrap，避免漏接 InitTracing）。flush 在優雅關閉時呼叫。
	flush := cmdutil.Bootstrap("cogito-agent")

	// 選擇 LLM provider（COGITO_PROVIDER：claude 預設 / openai 相容）。
	llmProvider, modelName, errProv := provider.FromEnv()
	if errProv != nil {
		log.Fatal(errProv)
	}
	log.Printf("[provider] model=%s", modelName)

	// 載入並連接 MCP 伺服器（設了 COGITO_MCP_CONFIG 才啟用）：外部 MCP 工具經 gateway 漸進式暴露、
	// 註冊進每個會話的 registry。連線是程式級長壽命的，優雅關閉時統一 Close（見結尾）。連不上的
	// server 略過、不影響啟動。改用 agentkit.LoadMCPGateway（與 cli/dashboard 同一套）——壞設定改
	// 成 warn+略過、不再 log.Fatal 拖垮整個 bot（一個 MCP 設定筆誤不該讓服務所有頻道全掛）。
	mcpGateway, mcpClients := agentkit.LoadMCPGateway(mcpDialTimeout)

	rootDir, _ := os.Getwd()
	rootDir += "/workspace" // 工作區根目錄；各頻道隔離到其下 channels/<id> 子目錄（見 bot.channelWorkDir）

	// 沙箱執行器：COGITO_SANDBOX=docker 時 bash 命令丟進隔離容器（OS 級硬邊界），否則宿主機直跑。
	executor := sandbox.FromEnv()
	log.Printf("[sandbox] bash 執行模式: %s", sandbox.Describe(executor))
	sandbox.WarnIfHost(executor) // bot＝開放入口：host 直跑時打醒目警告（見 WarnIfHost 的理由）

	// session 持久化：設 COGITO_SESSION_DIR 即把對話歷史/費用落地磁碟，重啟後按頻道 ID 復原。
	sessStore, sessDir := ctxpkg.StoreFromEnv() // 提出 if：search_sessions 工具也要用它檢索過去的對話
	if sessStore != nil {
		ctxpkg.GlobalSessionMgr.SetStore(sessStore)
		log.Printf("[Session] 持久化已啟用: %s", sessDir)
	} else {
		log.Printf("[Session] 純記憶體模式（設 COGITO_SESSION_DIR 可跨重啟續傳）")
	}

	// 背景任務管理器：每會話一個（session 級作用域），統一收集供優雅關閉時 kill 掉所有殘留行程。
	var taskMgrs []*tools.TaskManager
	var taskMgrsMu sync.Mutex

	// bot 先聲明後賦值：factory/中介層的閉包按引用捕獲 bot，工廠在服務啟動後才被呼叫，屆時已賦值。
	var bot *slackbot.SlackBot

	// engine factory —— 每個會話（頻道）現造引擎：工具 rooted 在【該會話自己的 WorkDir】
	// （per-channel 磁碟隔離的關鍵——不再全域共用一個 registry），並掛上專屬 CostTracker 與
	// 審批 middleware。EnableThinking=false（手動兩階段思考對 Claude 會退化成 <invoke> 文本）；
	// Slack 對話式入口預設不開 Plan Mode。
	// 高危操作審批 middleware（環繞式）：命中黑名單（如 bash rm -r / sudo / 覆蓋 .go）的工具呼叫
	// 會被掛起，把審批請求推回觸發它的 Slack 頻道（session.ID == channelID），等管理員回
	// approve/reject 才放行（不調 next 即短路）。抽成函式以便主工具池與子 agent只讀池共用
	//（子 agent 的 bash 同樣要過審批，不留後門）。
	// 政策檔（.claw/policy.json，選填）：可宣告 deny/ask/allow 覆蓋內建判斷。載入失敗直接退出——
	// 靜默忽略會讓人以為有保護、其實整份政策沒生效。
	pol, errPol := policy.Load(policy.ConfigPath(rootDir))
	if errPol != nil {
		log.Fatalf("[policy] 載入失敗（修好或移除 %s 再啟動）：%v", policy.ConfigPath(rootDir), errPol)
	}

	// 詢問人類：把審批請求推回觸發它的 Slack 頻道（session.ID == channelID），等管理員回
	// approve/reject。排程任務走 policy.WithUnattended 的 ctx，Guard 不會呼叫這裡（沒人可問）。
	askHuman := func(ctx context.Context, call schema.ToolCall) (bool, string) {
		channelID := ""
		if s := engine.SessionFromContext(ctx); s != nil {
			channelID = s.ID
		}
		return chatbot.GlobalApprovalMgr.WaitForApproval(call.ID, channelID, call.Name, string(call.Arguments), func(text string) {
			if channelID != "" {
				bot.SendMessage(channelID, text)
			}
		})
	}

	// 守門 middleware（環繞式）：Deny > Ask > Allow。抽成變數以便主工具池與子 agent只讀池共用
	//（子 agent 的 bash 同樣要過，不留後門）。
	approval := policy.Guard(pol, chatbot.IsDangerousCommand, askHuman)

	// 計時 middleware：記錄工具的物理執行耗時（如一個編譯 5 分鐘的 bash）。掛在 approval【之後】，
	// 故只量工具本身、不含人工審批等待。捕獲不修改 bash.go 等任何工具源碼（裝飾器攔截）。
	timing := tools.NewTimingMiddleware(func(toolName string, durationMs int64) {
		log.Printf("[Timing] 工具 %s 物理執行耗時 %dms\n", toolName, durationMs)
	})

	// COGITO_MEMORY_SCOPE=channel：長期記憶 per-conversation 隔離（技能仍共享）。預設 global＝現況。
	// 見 docs/multi-tenancy.md。memoryRootFor 集中一處決定「這個對話的記憶 root」。
	memScopeChannel := os.Getenv("COGITO_MEMORY_SCOPE") == "channel"
	memoryRootFor := func(sess *ctxpkg.Session) string {
		if memScopeChannel {
			return sess.WorkDir // channels/<id>/.claw/memory
		}
		return rootDir // 全 bot 共用
	}
	if memScopeChannel {
		log.Printf("[Session] 記憶隔離＝per-conversation（COGITO_MEMORY_SCOPE=channel）；技能仍共享")
	}

	// 背景反思用的 provider：設了 COGITO_REFLECT_MODEL 就換便宜模型跑（技能/記憶/KG 蒸餾是任務後的
	// 背景工作、產物還要人工放行，沒必要燒主模型）。未設＝沿用主 provider，行為不變。
	reflectProv := provider.ReflectProvider(llmProvider)
	if rm := reflectProv.ModelName(); rm != modelName {
		log.Printf("[evolve] 背景反思改用模型 %s（主模型 %s）", rm, modelName)
	}

	factory := func(sess *ctxpkg.Session, reporter engine.Reporter) *engine.AgentEngine {
		registry := tools.NewRegistry()
		memDir := memoryRootFor(sess)
		// 核心工具集：檔案讀寫/bash/編輯 rooted 在 sess.WorkDir（per-channel 磁碟隔離）；技能 rooted 在
		// rootDir（全 bot 共用）；長期記憶（recall）rooted 在 memDir（預設 rootDir，channel scope 時 per-對話）。
		agentkit.RegisterCoreTools(registry, sess.WorkDir, rootDir, memDir, executor)
		// 過去對話的檢索入口（跨 session／跨頻道）：排除自己，避免「覆盤自己的覆盤」。
		registry.Register(tools.NewSearchSessionsTool(sessStore, sess.ID))
		if selfEvolveEnabled() { // agent 可主動沉澱（與 post-task hook 互補；產物仍 gated）
			registry.Register(tools.NewConsolidateTool(reflectProv, rootDir, memDir, sess)) // 技能提案共享、記憶提案隨 scope
		}
		agentkit.RegisterMCPTools(registry, mcpGateway) // 外部 MCP 工具經 gateway 漸進式暴露
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

		// per-channel 模型覆蓋（`model <id>` 指令）：session 設了就用 Configurable 換模型；
		// CostTracker 以生效模型名計價。未設或 provider 不支援則沿用啟動預設。
		effProvider, effModel := llmProvider, modelName
		if m := sess.Model(); m != "" {
			if cfg, ok := llmProvider.(provider.Configurable); ok {
				effProvider, effModel = cfg.Configure(m, 0), m
			}
		}
		tracked := observability.NewCostTracker(effProvider, effModel, sess)
		// 對話式建構子＝滾動摘要 + history 有界化（bench/一次性任務走 NewAgentEngine 預設關，保持確定性）。
		eng := engine.NewConversationalEngine(tracked, registry, false)
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
		eng.MemoryDir = memDir // 記憶索引 root：預設＝AssetsDir(rootDir)，channel scope 時＝該對話 WorkDir

		// 子 agent工具池（超集）：read_file + bash 供探索；write_file + edit_file 供【實作型】具名
		// agent（須在 .claw/agents/<name>.md 的 tools 明確宣告才拿得到；預設探路者只取唯讀子集，見
		// defaultSubagentTools）。無 spawn_subagent（杜絕遞迴）。同掛審批——子 agent 的危險 bash /
		// 敏感寫入也要人工放行。抽成 factory 以支援 worktree 隔離（依 worktree 目錄重建同款工具）。
		// 子 agent工具池（超集）：read/bash 供探索、write/edit 供實作型具名 agent；同掛 approval/timing
		// 中介層（子 agent 的危險 bash / 敏感寫入也要人工放行、計時）。reporter 串進子 agent（進度以
		// 「[Subagent] …」前綴回報回頻道）。WithWorktreeIsolation 在 WireSubagent 內：isolation:worktree
		// 的 agent 在 git worktree 隔離跑、完事序列化 apply 回主工作區。skillsBaseDir=rootDir。
		agentkit.WireSubagent(registry, eng, sess.WorkDir, agentkit.SubagentOpts{
			Executor:      executor,
			SkillsBaseDir: rootDir,
			Reporter:      reporter,
			Middleware:    []tools.MiddlewareFunc{approval, timing},
			// 子 agent 也能用外部 MCP（如研究型子 agent 查資料源）。危險 MCP 呼叫仍過 approval 中介層。
			ExtraSubTools: func(r tools.Registry) { agentkit.RegisterMCPTools(r, mcpGateway) },
		})

		return eng
	}

	bot = slackbot.NewSlackBot(factory, rootDir)

	// Tier 4 自我進化（opt-in）：任務成功後反思軌跡。安全鐵律一致——產物只進【暫存區】、不自動生效，
	// 須人工 review（技能用 skillgate 晉升；提案記憶 apply 後放行為 .claw/memory/ 記錄才生效）。
	var skillSynth *evolve.SkillSynthesizer
	var memSynth *evolve.MemorySynthesizer
	if os.Getenv("COGITO_SKILL_SYNTH") == "1" {
		skillSynth = evolve.NewSkillSynthesizer(reflectProv, filepath.Join(rootDir, ".claw", evolve.ProposedSkillsDirName))
		log.Printf("[evolve] 技能自生成已啟用（寫入 .claw/%s，需人工 review）", evolve.ProposedSkillsDirName)
	}
	if os.Getenv("COGITO_MEMORY_SYNTH") == "1" {
		memSynth = evolve.NewMemorySynthesizer(reflectProv, rootDir)
		log.Printf("[evolve] 記憶自更新已啟用（寫入 .claw/%s，apply 後放行為長期記憶記錄）", evolve.ProposedMemoryFileName)
	}
	var kgExtract *evolve.RelationExtractor
	if os.Getenv("COGITO_KG_SYNTH") == "1" {
		kgExtract = evolve.NewRelationExtractor(reflectProv, rootDir)
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
				// 記憶提案寫進【該對話的記憶 root】（channel scope 時 per-conversation，否則共享 rootDir）——
				// 與 recall 讀路徑同源，跑後反思的產物才落在正確的租戶目錄。
				ms := memSynth
				if memScopeChannel {
					ms = evolve.NewMemorySynthesizer(reflectProv, memoryRootFor(session))
				}
				if added, err := ms.Reflect(ctx, taskPrompt, history); err != nil {
					log.Printf("[evolve] 記憶反思失敗（不影響任務）: %v", err)
				} else if len(added) > 0 {
					log.Printf("[evolve] 🧠 新增 %d 條提案記憶", len(added))
					chatbot.SendMessage(session.ID,
						memoryProposalMsg("慣例", added, evolve.PendingProposals(memoryRootFor(session))))
				}
			}
			if kgExtract != nil {
				ke := kgExtract
				if memScopeChannel {
					ke = evolve.NewRelationExtractor(reflectProv, memoryRootFor(session))
				}
				if n, err := ke.Extract(ctx); err != nil {
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
				ms := memSynth
				if memScopeChannel {
					ms = evolve.NewMemorySynthesizer(reflectProv, memoryRootFor(session))
				}
				if added, err := ms.ReflectFailure(ctx, taskPrompt, session.GetWorkingMemory(0), failureMsg); err != nil {
					log.Printf("[evolve] 失敗反思失敗（不影響任務）: %v", err)
				} else if len(added) > 0 {
					log.Printf("[evolve] 🧠 從失敗萃取 %d 條教訓", len(added))
					chatbot.SendMessage(session.ID,
						memoryProposalMsg("失敗教訓", added, evolve.PendingProposals(memoryRootFor(session))))
				}
			}
		}
	}
	// `learn` 手動蒸餾技能：獨立於自動 skill_synth 的 gating（explicit 使用者意圖，一律可用）；
	// 產物仍只進暫存區，須 skillgate 把關才生效。
	learnSynth := evolve.NewSkillSynthesizer(reflectProv, filepath.Join(rootDir, ".claw", evolve.ProposedSkillsDirName))
	learnHook := func(ctx context.Context, session *ctxpkg.Session) (string, error) {
		history := session.GetWorkingMemory(0)
		if len(history) == 0 {
			return "", nil
		}
		path, err := learnSynth.Reflect(ctx, firstUserContent(history), history)
		if err != nil || path == "" {
			return "", err
		}
		return filepath.Base(filepath.Dir(path)), nil // <slug>/SKILL.md → slug
	}

	// `memory reconcile`：掃既有記憶找矛盾/過時的，產出【可 diff 的提案】。與 learn 同理——
	// 手動觸發、便宜模型、產物只進提案通道。掛在 MEMORY_SYNTH 之下，因為它動的是同一份記憶庫。
	// memDir 由 Core 依會話給（多租戶下每個會話的記憶根目錄可能不同）。
	var reconcileHook chatbot.ReconcileHook
	if os.Getenv("COGITO_MEMORY_SYNTH") == "1" {
		reconcileHook = func(ctx context.Context, memDir string) ([]string, error) {
			return evolve.NewMemorySynthesizer(reflectProv, memDir).Reconcile(ctx)
		}
	}

	// 【入口平權】鉤子組一次、每個入口掛同一包——先前是三個 setter × 三個入口＝九處要記得接，
	// office HTTP 就漏了兩個（那邊派的工跑完不反思）。整包傳遞讓漏接變成編譯期問題。
	hooks := chatbot.Hooks{PostRun: postRun, PostFailure: postFailure, Learn: learnHook, Reconcile: reconcileHook}
	bot.SetHooks(hooks)

	// 像素辦公室 Web 外殼的 HTTP 派工入口（COGITO_HTTP_ADDR + COGITO_HTTP_TOKEN 都設定才開）。
	// 【必須在 hooks 組好之後】——先前擺在前面，結構上就不可能掛到鉤子。
	startOfficeHTTP(factory, rootDir, hooks, mcpGateway)

	// 監聽 SIGINT/SIGTERM 以優雅關閉：先停傳輸層（websocket/長輪詢隨 ctx 取消），再 flush OTel span。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 多平台（opt-in）：設了 TELEGRAM_BOT_TOKEN 就同時開 Telegram 長輪詢，與 Slack 同行程、共用
	// 同一 factory 與自我進化鉤子；會話/工作目錄【預設】靠 platform 前綴命名空間隔開（slack: vs
	// telegram:），但設了 COGITO_USER_LINK 時，已連結使用者的 DM 會刻意跨平台共用同一份狀態。
	if os.Getenv("TELEGRAM_BOT_TOKEN") != "" {
		tg := telegrambot.NewTelegramBot(factory, rootDir)
		tg.SetHooks(hooks)
		go tg.Start(ctx)
		tg.ResumeInterrupted() // 跨重啟續跑：續本次被硬砍中斷的 Telegram 任務（需 AUTO_RESUME + SESSION_DIR）
	}

	// cron 排程器：bot 是常駐行程，故排程掛在這裡才會「dashboard 關掉也照跑」。與 dashboard 端
	// 共用同一份 .claw/cron.json；跨行程檔案鎖保證同一輪只有一邊真的執行。沒有 job 就什麼都不做。
	go cron.New(rootDir, &botCronRunner{factory: factory, workDir: rootDir}, "bot").Run(ctx.Done())

	// Slack 走 Socket Mode（outbound websocket，免公開 URL）。兩平台都不需要對外連接埠，零基建。
	go bot.Start(ctx)
	bot.ResumeInterrupted() // 跨重啟續跑：續上次被硬砍中斷的 Slack 任務（需 AUTO_RESUME + SESSION_DIR）

	<-ctx.Done()
	log.Println("收到關閉信號，優雅關閉中...")
	stop()  // 取消 ctx → Slack websocket / Telegram 長輪詢各自收線
	flush() // flush OTel span（內部自帶 timeout + once 去重）
	for _, cl := range mcpClients {
		_ = cl.Close() // 結束 MCP 伺服器子行程，避免殘留
	}
	taskMgrsMu.Lock()
	for _, tm := range taskMgrs {
		tm.KillAll() // 收掉所有背景任務，避免殘留孤兒行程
	}
	taskMgrsMu.Unlock()
	if c, ok := executor.(interface{ Close() error }); ok {
		_ = c.Close() // 移除 per-session sandbox 容器（docker 模式）
	}
}

// firstUserContent 取歷史裡第一則使用者訊息內容（作為 /learn 蒸餾時的「任務」上下文）。
func firstUserContent(history []schema.Message) string {
	for _, m := range history {
		if m.Role == schema.RoleUser && m.ToolCallID == "" {
			return m.Content
		}
	}
	return "（本次對話）"
}

// memoryProposalMsg 組裝反思通知：直接列出內容 + 後續動作（閘在聊天視窗內，免去 cat 檔案）。
//
// 措辭要跟著 AUTOAPPLY 走。先前一律寫「（尚未生效）…回覆 apply memory 放行」，但開了自動放行
// 之後它們【早就生效了】——提案檔根本不存在，叫使用者去 apply 只會讓他對著空氣打指令，
// 更糟的是他會以為那些記憶還沒開始影響 agent。訊息說謊比訊息囉唆嚴重。
func memoryProposalMsg(kind string, added []string, pending int) string {
	auto := os.Getenv(evolve.EnvAutoApply) == "1"
	var b strings.Builder
	switch {
	case !auto:
		fmt.Fprintf(&b, "🧠 我從這次任務學到 %d 條*提案%s*（尚未生效）：\n", len(added), kind)
	case pending > 0:
		// 自動放行只吃專案慣例，使用者畫像那類會留在提案檔。一律說「已生效」就是說謊。
		fmt.Fprintf(&b, "🧠 我從這次任務學到 %d 條*%s*（專案慣例**已生效**；另有 %d 條關於你的偏好待你過目）：\n",
			len(added), kind, pending)
	default:
		fmt.Fprintf(&b, "🧠 我從這次任務學到 %d 條*%s*（**已生效**，之後會被 recall 取用）：\n", len(added), kind)
	}
	for _, l := range added {
		b.WriteString("• " + l + "\n")
	}
	switch {
	case !auto:
		b.WriteString("回覆 `apply memory` 放行為可檢索的長期記憶（存成記憶節點、recall 取用），或 `reject memory` 丟棄。")
	case pending > 0:
		b.WriteString("用 `memory list` 看那幾條，`apply memory <編號>` 放行或 `reject memory` 丟棄。")
	default:
		// 刻意講「刪檔」而不是某個指令：memory list/apply 那組管的是【提案】，
		// 已生效的記憶沒有對應的聊天指令。指一個不存在的操作比不指更糟。
		b.WriteString("覺得哪條不該記？記憶是一條一個檔，到 `.claw/memory/` 刪掉那個檔即可。")
	}
	return b.String()
}

// selfEvolveEnabled 回報是否啟用了任一自我進化開關——決定要不要把 consolidate 工具暴露給 agent。
func selfEvolveEnabled() bool {
	return os.Getenv("COGITO_SKILL_SYNTH") == "1" ||
		os.Getenv("COGITO_MEMORY_SYNTH") == "1" ||
		os.Getenv("COGITO_KG_SYNTH") == "1"
}
