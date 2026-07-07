// Package chatbot 是【與平台無關】的「聊天 → Agent」橋接核心：指令閘（approve/apply memory/edges）、
// 每頻道 session+磁碟隔離、per-WorkDir 鎖、跑任務管線、進度回報。Slack / Telegram 等只是薄傳輸層，
// 各自做入站解析 + 提供一個 send 函式，其餘共用本核心。多平台同進程時靠 platform 前綴命名空間
// （"slack:C123" vs "telegram:123"）杜絕 session / 工作目錄碰撞。
package chatbot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// 自動斷點續跑（opt-in，COGITO_AUTO_RESUME=1）：任務因【暫時性】錯誤中止時最多自動續跑幾次。
const maxAutoResume = 3

// maxCrossResume：跨重啟續跑的上限——同一任務連續 N 次「啟動時續跑又被硬砍」就停手，防崩潰迴圈燒錢。
const maxCrossResume = 3

// maxGoalContinue：goal 任務完成後 judge 驗收未過時，自動續跑的上限（防未達成無限續跑燒錢；
// 另有引擎的成本熔斷/回合上限兜底）。
const maxGoalContinue = 5

// resumeNudge 是續跑時補進歷史的系統提示（走 RoleUser，與成本軟著陸等系統提醒一致）。
const resumeNudge = "[系統] 先前因暫時性錯誤（如網路中斷）使任務未完成；連線現已恢復。請檢視目前進度，從中斷處接續完成，不要重做已完成的步驟。"

// EngineFactory 為每個會話動態組裝引擎（掛該頻道專屬 CostTracker；reporter 接給子智能體串流進度）。
type EngineFactory func(session *ctxpkg.Session, reporter engine.Reporter) *engine.AgentEngine

// PostRunHook 在任務【成功】後呼叫（如技能自生成反思）。可為 nil。
type PostRunHook func(ctx context.Context, session *ctxpkg.Session, taskPrompt string)

// PostFailureHook 在任務【失敗】後呼叫（live Reflexion）。可為 nil。
type PostFailureHook func(ctx context.Context, session *ctxpkg.Session, taskPrompt, failureMsg string)

// LearnHook 手動蒸餾技能（`learn` 指令）：從本會話軌跡反思出一個【提案】技能（進暫存區、gated）。
// 回傳提案技能名（空＝不值得保存，非錯誤）。可為 nil（未接則 /learn 提示未啟用）。
type LearnHook func(ctx context.Context, session *ctxpkg.Session) (skillName string, err error)

// senders：platform → 該平台的原生發送函式。讓 SendMessage 能用命名空間 convID 路由回正確傳輸層，
// 使審批/提案通知在「同進程多平台」下也送對人。ponytail: 全域 sync.Map 足矣（傳輸層數量極少）。
var senders sync.Map

// SendMessage 把命名空間 convID（"platform:rawID"）路由到擁有它的傳輸層發送。非命名空間字串直接忽略。
func SendMessage(convID, text string) {
	plat, raw, ok := strings.Cut(convID, ":")
	if !ok {
		return
	}
	if s, ok := senders.Load(plat); ok {
		s.(func(string, string))(raw, text)
	}
}

// Core 是傳輸無關的核心。每個傳輸層建一個，傳入自己的 platform 名與原生 send。
type Core struct {
	platform    string // 命名空間前綴；也用於工作目錄分層，杜絕跨平台碰撞
	workDir     string
	factory     EngineFactory
	postRun     PostRunHook
	postFailure PostFailureHook
	learn       LearnHook

	autoResume bool // 暫時性中斷後自動續跑（COGITO_AUTO_RESUME=1 開）

	allowedUsers map[string]bool // COGITO_ALLOWED_USERS：可驅動 agent 的使用者 id；空＝fail-closed 拒絕所有
	adminUsers   map[string]bool // COGITO_ADMIN_USERS：可 approve/reject 高危操作者；空則回退為 allowedUsers

	// running：per-WorkDir 執行中任務的取消函式（存在＝忙碌鎖）。同目錄序列化、不同頻道並行；
	// `/stop` 取出對應頻道的 cancel 中止其 Run。
	running   map[string]context.CancelFunc
	runningMu sync.Mutex
}

// NewCore 建核心並向全域 senders 註冊本平台的原生發送，使 SendMessage 路由可達。
func NewCore(platform, workDir string, factory EngineFactory, rawSend func(channelID, text string)) *Core {
	senders.Store(platform, rawSend)
	allowed := parseUserSet(os.Getenv("COGITO_ALLOWED_USERS"))
	admins := parseUserSet(os.Getenv("COGITO_ADMIN_USERS"))
	if len(admins) == 0 {
		admins = allowed // 未單獨設 admin：可對話者即可審批（fail-closed 已把陌生人擋在門外）
	}
	if len(allowed) == 0 {
		log.Printf("[%s] ⚠️ 未設 COGITO_ALLOWED_USERS，已 fail-closed 拒絕所有入站任務。請設環境變數（逗號分隔 user id）授權可用者。", platform)
	}
	return &Core{
		platform:     platform,
		workDir:      workDir,
		factory:      factory,
		autoResume:   os.Getenv("COGITO_AUTO_RESUME") == "1",
		allowedUsers: allowed,
		adminUsers:   admins,
		running:      make(map[string]context.CancelFunc),
	}
}

// parseUserSet 解析逗號分隔的 user id 清單成集合（去空白、略過空項）。
func parseUserSet(s string) map[string]bool {
	set := make(map[string]bool)
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			set[p] = true
		}
	}
	return set
}

func (c *Core) SetPostRunHook(h PostRunHook)         { c.postRun = h }
func (c *Core) SetPostFailureHook(h PostFailureHook) { c.postFailure = h }
func (c *Core) SetLearnHook(h LearnHook)             { c.learn = h }

// convID 把傳輸層的原生頻道 ID 加上平台前綴，成為全域唯一的會話標識。
func (c *Core) convID(channelID string) string { return c.platform + ":" + channelID }

// Dispatch 是兩個傳輸層共用的入站管線：傳入【原生】頻道 ID、發訊者 user id 與已清理的文本。
// 先過授權閘（fail-closed：不在 allowlist 一律拒絕），再依序試審批 / 記憶閘 / 關係閘口令
// （命中即消費、不佔鎖）；否則當新任務取鎖後背景開跑。
func (c *Core) Dispatch(channelID, userID, text string) {
	id := c.convID(channelID)
	// 【硬防線】入口授權：未授權者一律擋在門外——這是「開放 bot」唯一能安全的前提。
	if !c.allowedUsers[userID] {
		log.Printf("[%s] 🚫 拒絕未授權使用者 %s（頻道 %s）\n", c.platform, userID, channelID)
		SendMessage(id, fmt.Sprintf("🚫 未授權。你的使用者 ID：`%s`。請管理員將它加入 COGITO_ALLOWED_USERS 後再試。", userID))
		return
	}
	// 審批口令即便會話忙碌也要處理——這正是解開忙碌的方式。
	if c.tryResolveApproval(id, userID, text) {
		return
	}
	// 指令 gate：stop/status/model 等即時控制 + help + 自我進化 apply/reject + Plan Mode
	// （不佔鎖、不當成新任務；stop/status/model 即便忙碌也要能處理）。
	if c.tryStopCommand(id, text) || c.tryStatusCommand(id, text) || c.tryModelCommand(id, text) ||
		c.tryHelpCommand(id, text) || c.tryMemoryCommand(id, text) || c.tryEdgesCommand(id, text) ||
		c.tryConfigCommand(id, text) || c.tryPlanCommand(id, text) {
		return
	}
	// compress / learn / goal：會摺疊 session、蒸餾技能，或起一個持久目標任務（各自處理鎖）。命中即消費。
	if c.tryCompressCommand(id, text) || c.tryLearnCommand(id, text) || c.tryGoalCommand(id, text) {
		return
	}
	// 會話忙碌時拒絕新任務，避免併發 Run 汙染同一 session。
	workDir := c.channelWorkDir(id)
	ctx, cancel := context.WithCancel(context.Background())
	if !c.tryAcquire(workDir, cancel) {
		cancel()
		SendMessage(id, "⏳ 上一個任務仍在進行（或正在等待審批）。可用 `/stop` 中止，或稍候再發。")
		return
	}
	log.Printf("[%s] 收到 %s: %s\n", c.platform, channelID, text)
	go c.handleAgentRun(ctx, id, text, false)
}

// handleAgentRun 在背景跑一個任務：每個頻道 = 一個持久 Session（多輪記憶 + 跨頻道含磁碟隔離），
// 成功觸發 postRun、失敗觸發 postFailure。convID 已含平台前綴。
func (c *Core) handleAgentRun(ctx context.Context, convID, prompt string, goalTask bool) {
	workDir := c.channelWorkDir(convID)
	defer c.release(workDir) // 運行結束（含審批被拒/超時/崩潰/被 /stop 取消）釋放鎖並 cancel context

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		SendMessage(convID, fmt.Sprintf("❌ 無法建立工作目錄: %v", err))
		return
	}

	session := c.sessionFor(convID)
	session.SetRunning(true)        // 標記任務進行中；程序被硬砍時此旗標留在磁碟供啟動時掃出續跑
	defer session.SetRunning(false) // 正常結束（成功/終局失敗）都清掉——只有硬砍才會留 true
	session.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	reporter := &reporter{convID: convID}
	eng := c.factory(session, reporter)

	startCost := session.CostUSD() // 快照本次任務進入時的累計花費，收尾時報「本次」增量（session 是跨任務累加的）
	goalContinues := 0             // goal 任務驗收未過的自動續跑次數（封頂 maxGoalContinue）

	// 自動斷點續跑（opt-in）：任務因【暫時性】錯誤（網路中斷等）中止時，退避等待恢復後補一則系統
	// 續跑提示、帶完整歷史重跑，直到成功或重試用盡。回合/成本熔斷等【終局】錯誤不重試（重試只會再撞牆）。
	for attempt := 0; ; {
		err := eng.Run(ctx, session, reporter)
		if err == nil {
			session.ClearResume() // 成功 → 清跨重啟續跑計數（running 由上面的 defer 清）

			// goal 驗收：若本次是 goal 任務、目標仍在且未暫停，用 LLM judge 驗收；未達成且未達上限就
			// 把評語當反饋續跑（複用 Tier 2 的 JudgeGoal，與 CLI 的 -verify-judge 同一機制）。
			if goalTask && ctx.Err() == nil {
				if g := session.Goal(); g != "" && !session.GoalPaused() {
					done, reason, jerr := eng.JudgeGoal(ctx, session, g)
					switch {
					case jerr != nil:
						SendMessage(convID, fmt.Sprintf("⚠️ 目標驗收出錯（%v），視為完成（本次花費 $%.4f）。", jerr, session.CostUSD()-startCost))
						return
					case done:
						SendMessage(convID, fmt.Sprintf("✅ 目標達成（本次花費 $%.4f）：%s", session.CostUSD()-startCost, reason))
						if c.postRun != nil {
							c.postRun(context.Background(), session, prompt)
						}
						return
					case goalContinues < maxGoalContinue:
						goalContinues++
						SendMessage(convID, fmt.Sprintf("🎯 尚未達成，自動續跑（第 %d/%d 次）：%s", goalContinues, maxGoalContinue, reason))
						session.Append(schema.Message{Role: schema.RoleUser, Content: "目標尚未達成。驗收評語：" + reason + "\n請據此繼續，直到達成目標。"})
						continue
					default:
						SendMessage(convID, fmt.Sprintf("⚠️ 已達自動續跑上限（%d 次），目標仍未達成：%s（本次花費 $%.4f）。可補充指令或 `goal clear`。", maxGoalContinue, reason, session.CostUSD()-startCost))
						return
					}
				}
			}

			// 收尾訊號：給頻道一個明確的「完成」與本次花費，讓對話有結束感、成本也透明。
			SendMessage(convID, fmt.Sprintf("✅ 任務完成（本次花費 $%.4f）", session.CostUSD()-startCost))
			if c.postRun != nil {
				c.postRun(context.Background(), session, prompt)
			}
			return
		}
		// 使用者 /stop 取消：乾淨收尾（不當失敗、不觸發 postFailure、不自動續跑）。
		if errors.Is(err, context.Canceled) {
			session.ClearResume()
			SendMessage(convID, fmt.Sprintf("🛑 已中止本次任務（本次花費 $%.4f）。", session.CostUSD()-startCost))
			return
		}
		if c.autoResume && attempt < maxAutoResume && isRecoverableErr(err) {
			attempt++
			delay := resumeBackoff(attempt)
			SendMessage(convID, fmt.Sprintf("🔌 偵測到暫時性中斷（%v）。%.0f 秒後自動從斷點續跑（第 %d/%d 次）…", err, delay.Seconds(), attempt, maxAutoResume))
			time.Sleep(delay) // 退避＝等網路恢復；下一次 Run 的 API 呼叫即是探測
			session.Append(schema.Message{Role: schema.RoleUser, Content: resumeNudge})
			continue
		}
		SendMessage(convID, fmt.Sprintf("⚠️ 任務未能完成（本次花費 $%.4f）：%v", session.CostUSD()-startCost, err))
		if c.postFailure != nil {
			c.postFailure(context.Background(), session, prompt, err.Error())
		}
		return
	}
}

// isRecoverableErr 判斷任務中止是否屬「暫時性/可恢復」（網路類），值得等恢復後自動續跑。
// 採白名單（網路訊號）：net.Error，或錯誤鏈訊息含連線/逾時/5xx/429/overloaded 等徵兆。
// 回合/成本熔斷等【終局】錯誤不含這些徵兆，自然落到終局路徑——不會被誤判成可續跑而無限重試燒錢。
func isRecoverableErr(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, sig := range []string{
		"connection", "timeout", "deadline exceeded", "eof", "reset by peer",
		"no such host", "dial tcp", "i/o timeout", "temporarily", "broken pipe",
		"overloaded", "502", "503", "504", "429", "rate limit",
	} {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}

// resumeBackoff：指數退避 2s / 4s / 8s，給網路恢復留時間。
func resumeBackoff(attempt int) time.Duration {
	return time.Duration(int64(1)<<attempt) * time.Second
}

// sessionFor 取（或建）本頻道的持久 Session——指令閘與跑任務共用同一把 session。
func (c *Core) sessionFor(convID string) *ctxpkg.Session {
	return ctxpkg.GlobalSessionMgr.GetOrCreate(convID, c.channelWorkDir(convID))
}

// ResumeInterrupted 在程序啟動時掃描持久化的 session，把「上次被硬砍（OOM/SIGKILL/斷電）、任務仍
// 標記進行中」的自動從斷點續跑。只處理本平台（platform 前綴）的 session；連續多次續跑仍中斷則停手
// 防崩潰迴圈。需 COGITO_AUTO_RESUME=1 且有持久化（COGITO_SESSION_DIR），否則自然 no-op。
func (c *Core) ResumeInterrupted() {
	if !c.autoResume {
		return
	}
	for _, s := range ctxpkg.GlobalSessionMgr.ListInterrupted() {
		id := s.ID
		if !strings.HasPrefix(id, c.platform+":") {
			continue // 只續本平台的；別的平台由它自己的 core 處理
		}
		if s.ResumeAttempts() >= maxCrossResume {
			SendMessage(id, "⚠️ 上次中斷的任務已連續多次自動續跑仍未完成，已停止自動續跑。回覆任意訊息可手動接續。")
			s.ClearResume()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		if !c.tryAcquire(c.channelWorkDir(id), cancel) {
			cancel()
			continue // 該頻道已忙（極少見）→ 跳過，handleAgentRun 期望呼叫端已持鎖
		}
		s.BumpResumeAttempts()
		SendMessage(id, "🔄 偵測到程序上次中斷時有未完成的任務，正在從斷點自動續跑…")
		go c.handleAgentRun(ctx, id, resumeNudge, false)
	}
}

// helpText 是聊天端的指令一覽（**粗體**/`code` 由各平台 send 轉成該平台語法）。與 Dispatch 的
// 指令 gate 對齊——改動指令請同步這裡。大部分能力用自然語言交辦，這裡只列【框架級固定指令】。
const helpText = "🧭 **cogito-agent 指令一覽**\n\n" +
	"**交辦任務**：直接打字描述任務即可（群組請 @我 或回覆我）。危險操作會請你 approve/reject。\n\n" +
	"**執行控制**\n" +
	"`stop` — 中止本頻道正在跑的任務\n" +
	"`status` — 看本會話花費 / token / 歷史長度 / 模型\n" +
	"`model` / `model <id>` — 查看 / 切換本頻道模型（`model reset` 還原預設）\n" +
	"`goal <驗收標準>` — 設一個目標，我追到 judge 判定達成為止（`goal status/pause/resume/clear` 管理）\n" +
	"`compress` — 手動摺疊 context，縮短歷史省成本\n" +
	"`learn` — 從本次對話蒸餾一個【提案】技能（過 skillgate 把關才生效）\n\n" +
	"**審批（HITL）**\n" +
	"`approve` / `reject` — 放行 / 否決待審批的高危操作（僅管理員；可帶 ID：`approve <id>`）\n\n" +
	"**Plan Mode（長任務斷點續傳）**\n" +
	"`plan on` / `plan off` / `plan status` — 把計畫/進度外部化到 PLAN.md · TODO.md 的開關\n\n" +
	"**自我進化把關（提案 → 人工放行）**\n" +
	"`apply memory` / `reject memory` — 放行 / 丟棄提案的長期記憶\n" +
	"`apply edges` / `reject edges` — 放行 / 丟棄提案的知識圖譜關係\n" +
	"`apply config` / `reject config` — 套用 / 丟棄調參提案\n\n" +
	"**說明**\n" +
	"`help` — 顯示本清單\n\n" +
	"（其餘能力用自然語言交辦：讀寫檔案、bash、長期記憶 recall、派子 agent、畫長條圖、呼叫 MCP 工具…）"

// tryHelpCommand：顯示指令一覽。命中即消費。
func (c *Core) tryHelpCommand(convID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "help", "/help", "?", "指令", "commands", "/commands":
		SendMessage(convID, helpText)
		return true
	}
	return false
}

// isRunning 回報某頻道是否有任務執行中（供 /status）。
func (c *Core) isRunning(workDir string) bool {
	c.runningMu.Lock()
	_, ok := c.running[workDir]
	c.runningMu.Unlock()
	return ok
}

// tryStopCommand：中止本頻道正在執行的任務（`/stop`）。命中即消費（即便忙碌也要能處理——這正是重點）。
func (c *Core) tryStopCommand(convID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "stop", "/stop", "中止", "停":
		if c.stop(c.channelWorkDir(convID)) {
			SendMessage(convID, "🛑 正在中止本頻道的任務…")
		} else {
			SendMessage(convID, "ℹ️ 目前沒有正在執行的任務。")
		}
		return true
	}
	return false
}

// tryStatusCommand：顯示本會話狀態（花費/token/歷史長度/模型/Plan/忙碌）。
func (c *Core) tryStatusCommand(convID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "status", "/status", "狀態":
		s := c.sessionFor(convID)
		pt, ct, cost := s.Usage()
		model := s.Model()
		if model == "" {
			model = "（預設）"
		}
		plan := "關"
		if s.PlanMode() {
			plan = "開"
		}
		state := "閒置"
		if c.isRunning(c.channelWorkDir(convID)) {
			state = "執行中（可 `/stop`）"
		}
		SendMessage(convID, fmt.Sprintf("📊 **本會話狀態**\n累計花費：$%.4f\nToken：輸入 %d / 輸出 %d\n歷史訊息：%d 則\n模型：%s\nPlan Mode：%s\n狀態：%s",
			cost, pt, ct, s.HistoryLen(), model, plan, state))
		return true
	}
	return false
}

// tryModelCommand：查看/切換本頻道模型（`model` 查看；`model <id>` 設定；`model reset` 還原預設）。
func (c *Core) tryModelCommand(convID, text string) bool {
	t := strings.TrimSpace(text)
	low := strings.ToLower(t)
	if low != "model" && low != "/model" && low != "模型" &&
		!strings.HasPrefix(low, "model ") && !strings.HasPrefix(low, "/model ") && !strings.HasPrefix(low, "模型 ") {
		return false
	}
	arg := ""
	if i := strings.IndexByte(t, ' '); i >= 0 {
		arg = strings.TrimSpace(t[i+1:])
	}
	s := c.sessionFor(convID)
	switch {
	case arg == "":
		cur := s.Model()
		if cur == "" {
			cur = "（預設，啟動時設定）"
		}
		SendMessage(convID, fmt.Sprintf("🧠 本頻道模型：%s\n切換：`model <模型id>`（如 `model claude-haiku-4-5`）；還原：`model reset`", cur))
	case strings.EqualFold(arg, "reset"), strings.EqualFold(arg, "default"):
		s.SetModel("")
		SendMessage(convID, "🧠 已還原為啟動預設模型（下個任務生效）。")
	default:
		s.SetModel(arg)
		SendMessage(convID, fmt.Sprintf("🧠 本頻道模型已設為 `%s`（下個任務生效）。provider 不支援該模型時，下個任務會回錯。", arg))
	}
	return true
}

// tryCompressCommand：手動摺疊本會話 context（`compress`）。走 goroutine（有 LLM 呼叫）、取鎖與任務
// 序列化——避免與正在跑的任務同時改 session。命中即消費。
func (c *Core) tryCompressCommand(convID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "compress", "/compress", "壓縮":
	default:
		return false
	}
	workDir := c.channelWorkDir(convID)
	ctx, cancel := context.WithCancel(context.Background())
	if !c.tryAcquire(workDir, cancel) {
		cancel()
		SendMessage(convID, "⏳ 任務進行中，請稍候或先 `/stop` 再壓縮。")
		return true
	}
	go func() {
		defer c.release(workDir)
		session := c.sessionFor(convID)
		eng := c.factory(session, &reporter{convID: convID})
		n, err := eng.ForceSummary(ctx, session)
		switch {
		case err != nil:
			SendMessage(convID, fmt.Sprintf("❌ 壓縮失敗：%v", err))
		case n == 0:
			SendMessage(convID, "ℹ️ 目前歷史已很短，無需壓縮。")
		default:
			SendMessage(convID, fmt.Sprintf("🗜️ 已把 %d 則舊訊息摺進滾動摘要，context 更精簡了。", n))
		}
	}()
	return true
}

// tryLearnCommand：手動從本會話蒸餾一個【提案】技能（`learn`）。走 goroutine（有 LLM 呼叫）、取鎖序列化。
func (c *Core) tryLearnCommand(convID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "learn", "/learn", "學習":
	default:
		return false
	}
	if c.learn == nil {
		SendMessage(convID, "ℹ️ 技能蒸餾未接（本部署未啟用）。")
		return true
	}
	workDir := c.channelWorkDir(convID)
	ctx, cancel := context.WithCancel(context.Background())
	if !c.tryAcquire(workDir, cancel) {
		cancel()
		SendMessage(convID, "⏳ 任務進行中，請稍候或先 `/stop` 再 learn。")
		return true
	}
	go func() {
		defer c.release(workDir)
		name, err := c.learn(ctx, c.sessionFor(convID))
		switch {
		case err != nil:
			SendMessage(convID, fmt.Sprintf("❌ 蒸餾技能失敗：%v", err))
		case name == "":
			SendMessage(convID, "ℹ️ 這段對話沒有值得沉澱成可複用技能的流程。")
		default:
			SendMessage(convID, fmt.Sprintf("📚 已從本次對話蒸餾出【提案技能】`%s`（進暫存區、未生效）。用 `go run ./cmd/skillgate` 把關晉升後才會被 agent 使用。", name))
		}
	}()
	return true
}

// tryGoalCommand：持久目標（`goal <text>` 設目標並立即起任務追它；`goal status/pause/resume/clear` 管理）。
// `goal <text>` 走 handleAgentRun 的 goal 迴圈：每輪完成後 judge 驗收、未達成續跑（封頂 maxGoalContinue）。
func (c *Core) tryGoalCommand(convID, text string) bool {
	t := strings.TrimSpace(text)
	low := strings.ToLower(t)
	if low != "goal" && low != "/goal" && low != "目標" &&
		!strings.HasPrefix(low, "goal ") && !strings.HasPrefix(low, "/goal ") && !strings.HasPrefix(low, "目標 ") {
		return false
	}
	arg := ""
	if i := strings.IndexByte(t, ' '); i >= 0 {
		arg = strings.TrimSpace(t[i+1:])
	}
	s := c.sessionFor(convID)
	switch {
	case arg == "", strings.EqualFold(arg, "status"), arg == "?":
		g := s.Goal()
		if g == "" {
			SendMessage(convID, "🎯 目前沒有設定目標。用 `goal <驗收標準>` 設一個，我會追到達成為止（每輪完成自動驗收、未達成續跑）。")
		} else {
			state := "追蹤中"
			if s.GoalPaused() {
				state = "已暫停"
			}
			SendMessage(convID, fmt.Sprintf("🎯 目前目標（%s）：%s\n`goal pause`/`goal resume` 暫停/恢復自動續跑、`goal clear` 清除。", state, g))
		}
		return true
	case strings.EqualFold(arg, "pause"):
		s.SetGoalPaused(true)
		SendMessage(convID, "⏸️ 已暫停 goal 自動續跑（目標仍保留，下次 `goal <同一標準>` 或 `goal resume` 再追）。")
		return true
	case strings.EqualFold(arg, "resume"):
		s.SetGoalPaused(false)
		SendMessage(convID, "▶️ 已恢復 goal 自動續跑。")
		return true
	case strings.EqualFold(arg, "clear"), strings.EqualFold(arg, "off"):
		s.ClearGoal()
		SendMessage(convID, "🎯 已清除目標。")
		return true
	default:
		// goal <text>：設目標 + 立即起一個 goal 任務追它（自己取鎖、走 goal 迴圈）。
		s.SetGoal(arg)
		workDir := c.channelWorkDir(convID)
		ctx, cancel := context.WithCancel(context.Background())
		if !c.tryAcquire(workDir, cancel) {
			cancel()
			SendMessage(convID, "🎯 目標已記下，但目前有任務進行中。可 `/stop` 中止後打 `goal "+arg+"` 讓我開始追。")
			return true
		}
		SendMessage(convID, fmt.Sprintf("🎯 目標已設定，開始追蹤（每輪完成後自動驗收，未達成最多續跑 %d 次）：\n%s", maxGoalContinue, arg))
		go c.handleAgentRun(ctx, convID, arg, true)
		return true
	}
}

// tryConfigCommand：apply/reject config 閘——把 `cmd/bench -tune` 產出的提案參數（config.proposed.json）
// 過人工閘晉升為執行期 config.json（引擎啟動時讀）。閉合參數自調飛輪：tune→propose→人工 apply→生效。
func (c *Core) tryConfigCommand(convID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "apply config", "approve config":
		changes, err := evolve.ApplyProposedConfig(c.workDir)
		switch {
		case err != nil:
			SendMessage(convID, fmt.Sprintf("❌ 套用參數失敗: %v", err))
		case len(changes) == 0:
			SendMessage(convID, "ℹ️ 目前沒有提案參數。")
		default:
			SendMessage(convID, "✅ 已套用調參提案（下次任務起生效）：\n• "+strings.Join(changes, "\n• "))
		}
		return true
	case "reject config", "discard config":
		if had, _ := evolve.DiscardProposedConfig(c.workDir); had {
			SendMessage(convID, "🗑️ 已丟棄提案參數（未套用）。")
		} else {
			SendMessage(convID, "ℹ️ 目前沒有提案參數。")
		}
		return true
	}
	return false
}

// tryPlanCommand：per-channel Plan Mode 切換——`plan on`/`plan off`/`plan status`。狀態存在 Session
// （隨之落盤），factory 建引擎時讀 session.PlanMode()。命中即消費。
func (c *Core) tryPlanCommand(convID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "plan on", "plan mode on":
		c.sessionFor(convID).SetPlanMode(true)
		SendMessage(convID, "🗺️ 已為本頻道開啟 Plan Mode：之後的任務會把計畫/進度外部化到 PLAN.md / TODO.md，並啟用『原始目標錨』與『確定性步驟跳過』。適合多步長任務、可斷點續跑。關閉打 `plan off`。")
		return true
	case "plan off", "plan mode off":
		c.sessionFor(convID).SetPlanMode(false)
		SendMessage(convID, "🗺️ 已關閉本頻道 Plan Mode，回一般對話模式（短任務/閒聊免計畫檔的儀式）。")
		return true
	case "plan status", "plan?", "plan":
		if c.sessionFor(convID).PlanMode() {
			SendMessage(convID, "🗺️ 本頻道 Plan Mode：**開**。打 `plan off` 關閉。")
		} else {
			SendMessage(convID, "🗺️ 本頻道 Plan Mode：**關**。打 `plan on` 開啟（多步長任務建議開）。")
		}
		return true
	}
	return false
}

// tryMemoryCommand：apply/reject memory 閘——人點頭才把提案記憶放行為可檢索長期記憶（.claw/memory/）。
func (c *Core) tryMemoryCommand(convID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "apply memory", "approve memory":
		applied, err := evolve.ApplyProposedMemory(c.workDir)
		switch {
		case err != nil:
			SendMessage(convID, fmt.Sprintf("❌ 併入失敗: %v", err))
		case applied == "":
			SendMessage(convID, "ℹ️ 目前沒有提案記憶。")
		default:
			SendMessage(convID, "✅ 已把提案記憶放行為可檢索的長期記憶（recall 可取），下次任務起生效。")
		}
		return true
	case "reject memory", "discard memory":
		if had, _ := evolve.DiscardProposedMemory(c.workDir); had {
			SendMessage(convID, "🗑️ 已丟棄提案記憶（未放行）。")
		} else {
			SendMessage(convID, "ℹ️ 目前沒有提案記憶。")
		}
		return true
	}
	return false
}

// tryEdgesCommand：apply/reject edges 閘——把 post-task 抽出的 typed 關係過 gate 併入知識圖譜。
func (c *Core) tryEdgesCommand(convID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "apply edges", "approve edges":
		applied, rejected, err := ctxpkg.ApplyProposedEdges(c.workDir)
		switch {
		case err != nil:
			SendMessage(convID, fmt.Sprintf("❌ 套用關係失敗: %v", err))
		case applied == 0 && rejected == 0:
			SendMessage(convID, "ℹ️ 目前沒有提案關係。")
		default:
			SendMessage(convID, fmt.Sprintf("✅ 已把 %d 條提案關係併入知識圖譜（gate 拒 %d 條），下次 recall 起生效。", applied, rejected))
		}
		return true
	case "reject edges", "discard edges":
		if had, _ := ctxpkg.DiscardProposedEdges(c.workDir); had {
			SendMessage(convID, "🗑️ 已丟棄提案關係（未放行）。")
		} else {
			SendMessage(convID, "ℹ️ 目前沒有提案關係。")
		}
		return true
	}
	return false
}

// tryResolveApproval 攔截 approve/reject 口令（裸口令按本頻道解析，亦兼容 approve <taskID>）。命中即消費。
// 只有管理員（COGITO_ADMIN_USERS）能放行/否決高危操作——杜絕「發起者＝批准者」自我放行繞過人在迴路。
func (c *Core) tryResolveApproval(convID, userID, text string) bool {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)

	var allowed bool
	var reason string
	switch {
	case lower == "approve" || strings.HasPrefix(lower, "approve "):
		allowed, reason = true, "人類管理員已批准操作"
	case lower == "reject" || strings.HasPrefix(lower, "reject "):
		allowed, reason = false, "人類管理員認為風險過高，已拒絕該操作"
	default:
		return false
	}

	// 命中口令但非管理員：消費掉並拒絕，別讓發起者自我放行。
	if !c.adminUsers[userID] {
		log.Printf("[%s] 🚫 非管理員 %s 嘗試 %s，已拒絕\n", c.platform, userID, lower)
		SendMessage(convID, "🚫 只有管理員可以 approve/reject 高危操作。")
		return true
	}

	idPart := strings.TrimSpace(text[strings.IndexByte(text, ' ')+1:])
	if !strings.ContainsRune(text, ' ') {
		idPart = ""
	}
	if idPart != "" {
		if !GlobalApprovalMgr.ResolveApproval(idPart, allowed, reason) {
			SendMessage(convID, fmt.Sprintf("⚠️ 未找到待審批任務 `%s`（可能已超時或已處理）。", idPart))
		}
		return true
	}
	switch n := GlobalApprovalMgr.ResolveByChannel(convID, allowed, reason); {
	case n == 0:
		SendMessage(convID, "ℹ️ 當前沒有待審批的操作。")
	case n > 1:
		verb := "批准"
		if !allowed {
			verb = "拒絕"
		}
		SendMessage(convID, fmt.Sprintf("已對本頻道 %d 個待審批操作執行%s。", n, verb))
	}
	return true
}

// channelWorkDir 把每個頻道隔離到 workDir/channels/<platform>/<sanitized> 子目錄；platform 分層
// 杜絕跨平台同 ID 碰撞。channelID（已含前綴）經 sanitizeSegment 清理，杜絕路徑穿越。
func (c *Core) channelWorkDir(channelID string) string {
	return filepath.Join(c.workDir, "channels", sanitizeSegment(channelID))
}

// sanitizeSegment 把字串清成單一安全路徑片段：非 [A-Za-z0-9_-] 一律換 '_'，結果不含 '/'、'.'。
func sanitizeSegment(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	if sb.Len() == 0 {
		return "default"
	}
	return sb.String()
}

// tryAcquire 取鎖並登記該任務的取消函式（供 /stop 中止）。已忙碌則回 false。
func (c *Core) tryAcquire(workDir string, cancel context.CancelFunc) bool {
	c.runningMu.Lock()
	defer c.runningMu.Unlock()
	if _, ok := c.running[workDir]; ok {
		return false
	}
	c.running[workDir] = cancel
	return true
}

// release 解鎖並取消 context（釋放資源；對已結束的 context 是 no-op）。
func (c *Core) release(workDir string) {
	c.runningMu.Lock()
	if cancel, ok := c.running[workDir]; ok {
		cancel()
		delete(c.running, workDir)
	}
	c.runningMu.Unlock()
}

// stop 取消某頻道正在執行的任務（`/stop` 用）。回傳是否真的有任務被取消。
// 取消後 Run 收到 ctx 取消而中止，由 handleAgentRun 的 defer release 收尾。
func (c *Core) stop(workDir string) bool {
	c.runningMu.Lock()
	cancel, ok := c.running[workDir]
	c.runningMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// reporter 把引擎逐步進度回推到頻道（透過 SendMessage 路由）。與平台無關：訊息文本一致。
type reporter struct{ convID string }

func (r *reporter) OnThinking(ctx context.Context) {
	SendMessage(r.convID, "🤔 模型正在慢思考 (Thinking)...")
}

func (r *reporter) OnToolCall(ctx context.Context, toolName, args string) {
	// 參數壓成單行預覽再送——否則 write_file 寫大檔、長 bash 腳本會把整包內容原封貼進頻道洗版。
	SendMessage(r.convID, fmt.Sprintf("🛠️ *正在執行工具*：`%s`\n參數：`%s`", toolName, argPreview(args, 200)))
}

func (r *reporter) OnToolResult(ctx context.Context, toolName, result string, isError bool) {
	if isError {
		// 錯誤保留多行結構（便於讀 stderr），但仍封頂長度，避免 10KB 報錯淹沒頻道。
		SendMessage(r.convID, fmt.Sprintf("⚠️ *執行報錯* (%s)：\n%s", toolName, capRunes(result, 800)))
	} else {
		SendMessage(r.convID, fmt.Sprintf("✅ *執行成功* (%s)", toolName))
	}
}

// capRunes rune-safe 截斷（不切壞多位元組字元），超長補省略標記。
func capRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…（已截斷）"
	}
	return s
}

// argPreview 把工具參數壓成單行預覽：去換行 + rune-safe 截斷，用於聊天進度回報。
func argPreview(s string, max int) string {
	return capRunes(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "), max)
}

func (r *reporter) OnMessage(ctx context.Context, content string) {
	// 最終回覆走格式化：GFM 表格→等寬對齊 block、## 標題→粗體。傳輸層再把 ``` / **粗體** 轉成
	// 各平台語法（Slack 原生吃、Telegram 用 HTML）。進度訊息不經這裡，維持精簡。
	SendMessage(r.convID, formatForChat(content))
}

// formatForChat 把模型輸出的 GitHub Markdown 轉成【聊天平台通用】的中介格式：Markdown 表格兩個
// 平台都不 render，改成空格對齊（CJK 算 2 格寬）的等寬 code block；## 標題轉 **粗體**。輸出仍用
// ``` 與 **x** 當中性標記，由各傳輸層的 send 落地成該平台語法。
func formatForChat(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if isTableRow(lines[i]) && i+1 < len(lines) && isTableSeparator(lines[i+1]) {
			block, next := renderTable(lines, i)
			out = append(out, block)
			i = next - 1
			continue
		}
		out = append(out, headerToBold(lines[i]))
	}
	return strings.Join(out, "\n")
}

func isTableRow(line string) bool {
	return strings.Contains(line, "|") && strings.TrimSpace(line) != ""
}

// isTableSeparator 判斷是否為 GFM 表格的分隔列（只含 - : | 與空白，且至少一個 -）。
func isTableSeparator(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || !strings.Contains(t, "-") {
		return false
	}
	return strings.Trim(t, "-:| ") == ""
}

// headerToBold 把 `## 標題` 轉成 `**標題**`（聊天平台不 render #）。
func headerToBold(line string) string {
	t := strings.TrimLeft(line, " ")
	h := 0
	for h < len(t) && t[h] == '#' {
		h++
	}
	if h > 0 && h <= 6 && h < len(t) && t[h] == ' ' {
		return "**" + strings.TrimSpace(t[h+1:]) + "**"
	}
	return line
}

// renderTable 從 start（表頭列）起把一段 GFM 表格轉成等寬對齊、包在 ``` 內的 block。
// 回傳 block 與「表格結束後的下一列索引」。欄寬用顯示寬度（CJK=2）計算，才不會在等寬字型下歪掉。
func renderTable(lines []string, start int) (string, int) {
	parse := func(l string) []string {
		t := strings.TrimSpace(l)
		t = strings.TrimPrefix(t, "|")
		t = strings.TrimSuffix(t, "|")
		cells := strings.Split(t, "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		return cells
	}
	header := parse(lines[start])
	var rows [][]string
	end := start + 2 // 跳過表頭 + 分隔列
	for end < len(lines) && isTableRow(lines[end]) {
		rows = append(rows, parse(lines[end]))
		end++
	}
	// 欄數取所有列的最大值；欄寬取該欄所有 cell 顯示寬度的最大值。
	cols := len(header)
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	width := make([]int, cols)
	measure := func(cells []string) {
		for i, c := range cells {
			if w := dispWidth(c); w > width[i] {
				width[i] = w
			}
		}
	}
	measure(header)
	for _, r := range rows {
		measure(r)
	}
	pad := func(cells []string) string {
		parts := make([]string, cols)
		for i := 0; i < cols; i++ {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			parts[i] = c + strings.Repeat(" ", width[i]-dispWidth(c))
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	var b strings.Builder
	b.WriteString("```\n")
	b.WriteString(pad(header) + "\n")
	sep := make([]string, cols)
	for i := range sep {
		sep[i] = strings.Repeat("─", width[i])
	}
	b.WriteString(strings.Join(sep, "  ") + "\n")
	for _, r := range rows {
		b.WriteString(pad(r) + "\n")
	}
	b.WriteString("```")
	return b.String(), end
}

// dispWidth 回傳字串在等寬字型下的顯示寬度：CJK/全形字算 2 格，其餘 1 格。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWide(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func isWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || // Hangul Jamo
		(r >= 0x2E80 && r <= 0x303E) || // CJK 部首/康熙
		(r >= 0x3041 && r <= 0x33FF) || // 平假名/片假名/CJK 符號
		(r >= 0x3400 && r <= 0x4DBF) || // CJK 擴充 A
		(r >= 0x4E00 && r <= 0x9FFF) || // CJK 統一表意
		(r >= 0xAC00 && r <= 0xD7A3) || // 韓文音節
		(r >= 0xF900 && r <= 0xFAFF) || // CJK 相容
		(r >= 0xFE30 && r <= 0xFE4F) || // CJK 相容形式
		(r >= 0xFF00 && r <= 0xFF60) || // 全形 ASCII
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x20000 && r <= 0x3FFFD) // CJK 擴充 B+
}

// OnTurn 不回推（回合計數對使用者是噪音）。
func (r *reporter) OnTurn(ctx context.Context, turn int) {}

var _ engine.Reporter = (*reporter)(nil)
