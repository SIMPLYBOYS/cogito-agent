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

// resumeNudge 是續跑時補進歷史的系統提示（走 RoleUser，與成本軟著陸等系統提醒一致）。
const resumeNudge = "[系統] 先前因暫時性錯誤（如網路中斷）使任務未完成；連線現已恢復。請檢視目前進度，從中斷處接續完成，不要重做已完成的步驟。"

// EngineFactory 為每個會話動態組裝引擎（掛該頻道專屬 CostTracker；reporter 接給子智能體串流進度）。
type EngineFactory func(session *ctxpkg.Session, reporter engine.Reporter) *engine.AgentEngine

// PostRunHook 在任務【成功】後呼叫（如技能自生成反思）。可為 nil。
type PostRunHook func(ctx context.Context, session *ctxpkg.Session, taskPrompt string)

// PostFailureHook 在任務【失敗】後呼叫（live Reflexion）。可為 nil。
type PostFailureHook func(ctx context.Context, session *ctxpkg.Session, taskPrompt, failureMsg string)

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

	autoResume bool // 暫時性中斷後自動續跑（COGITO_AUTO_RESUME=1 開）

	allowedUsers map[string]bool // COGITO_ALLOWED_USERS：可驅動 agent 的使用者 id；空＝fail-closed 拒絕所有
	adminUsers   map[string]bool // COGITO_ADMIN_USERS：可 approve/reject 高危操作者；空則回退為 allowedUsers

	busy   map[string]bool // per-WorkDir 鎖：同目錄序列化、不同頻道並行
	busyMu sync.Mutex
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
		busy:         make(map[string]bool),
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
	// 自我進化的閘：apply/reject memory|edges|config，與 per-channel Plan Mode 切換（不佔鎖、不當成新任務）。
	if c.tryMemoryCommand(id, text) || c.tryEdgesCommand(id, text) || c.tryConfigCommand(id, text) || c.tryPlanCommand(id, text) {
		return
	}
	// 會話忙碌時拒絕新任務，避免併發 Run 汙染同一 session。
	if !c.tryAcquire(c.channelWorkDir(id)) {
		SendMessage(id, "⏳ 上一個任務仍在進行（或正在等待審批），請先處理審批或稍候再發。")
		return
	}
	log.Printf("[%s] 收到 %s: %s\n", c.platform, channelID, text)
	go c.handleAgentRun(id, text)
}

// handleAgentRun 在背景跑一個任務：每個頻道 = 一個持久 Session（多輪記憶 + 跨頻道含磁碟隔離），
// 成功觸發 postRun、失敗觸發 postFailure。convID 已含平台前綴。
func (c *Core) handleAgentRun(convID, prompt string) {
	workDir := c.channelWorkDir(convID)
	defer c.release(workDir) // 運行結束（含審批被拒/超時/崩潰）釋放鎖

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

	// 自動斷點續跑（opt-in）：任務因【暫時性】錯誤（網路中斷等）中止時，退避等待恢復後補一則系統
	// 續跑提示、帶完整歷史重跑，直到成功或重試用盡。回合/成本熔斷等【終局】錯誤不重試（重試只會再撞牆）。
	for attempt := 0; ; {
		err := eng.Run(context.Background(), session, reporter)
		if err == nil {
			session.ClearResume() // 成功 → 清跨重啟續跑計數（running 由上面的 defer 清）
			if c.postRun != nil {
				c.postRun(context.Background(), session, prompt)
			}
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
		SendMessage(convID, fmt.Sprintf("❌ Agent 運行崩潰: %v", err))
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
		if !c.tryAcquire(c.channelWorkDir(id)) {
			continue // 該頻道已忙（極少見）→ 跳過，handleAgentRun 期望呼叫端已持鎖
		}
		s.BumpResumeAttempts()
		SendMessage(id, "🔄 偵測到程序上次中斷時有未完成的任務，正在從斷點自動續跑…")
		go c.handleAgentRun(id, resumeNudge)
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

func (c *Core) tryAcquire(workDir string) bool {
	c.busyMu.Lock()
	defer c.busyMu.Unlock()
	if c.busy[workDir] {
		return false
	}
	c.busy[workDir] = true
	return true
}

func (c *Core) release(workDir string) {
	c.busyMu.Lock()
	delete(c.busy, workDir)
	c.busyMu.Unlock()
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

func (r *reporter) OnMessage(ctx context.Context, content string) { SendMessage(r.convID, content) }

// OnTurn 不回推（回合計數對使用者是噪音）。
func (r *reporter) OnTurn(ctx context.Context, turn int) {}

var _ engine.Reporter = (*reporter)(nil)
