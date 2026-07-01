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

	busy   map[string]bool // per-WorkDir 鎖：同目錄序列化、不同頻道並行
	busyMu sync.Mutex
}

// NewCore 建核心並向全域 senders 註冊本平台的原生發送，使 SendMessage 路由可達。
func NewCore(platform, workDir string, factory EngineFactory, rawSend func(channelID, text string)) *Core {
	senders.Store(platform, rawSend)
	return &Core{
		platform:   platform,
		workDir:    workDir,
		factory:    factory,
		autoResume: os.Getenv("COGITO_AUTO_RESUME") == "1",
		busy:       make(map[string]bool),
	}
}

func (c *Core) SetPostRunHook(h PostRunHook)         { c.postRun = h }
func (c *Core) SetPostFailureHook(h PostFailureHook) { c.postFailure = h }

// convID 把傳輸層的原生頻道 ID 加上平台前綴，成為全域唯一的會話標識。
func (c *Core) convID(channelID string) string { return c.platform + ":" + channelID }

// Dispatch 是兩個傳輸層共用的入站管線：傳入【原生】頻道 ID 與已清理的文本。
// 依序試審批 / 記憶閘 / 關係閘口令（命中即消費、不佔鎖）；否則當新任務取鎖後背景開跑。
func (c *Core) Dispatch(channelID, text string) {
	id := c.convID(channelID)
	// 審批口令即便會話忙碌也要處理——這正是解開忙碌的方式。
	if c.tryResolveApproval(id, text) {
		return
	}
	// 自我進化的閘：apply/reject memory|edges（不佔鎖、不當成新任務）。
	if c.tryMemoryCommand(id, text) || c.tryEdgesCommand(id, text) {
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

	session := ctxpkg.GlobalSessionMgr.GetOrCreate(convID, workDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	reporter := &reporter{convID: convID}
	eng := c.factory(session, reporter)

	// 自動斷點續跑（opt-in）：任務因【暫時性】錯誤（網路中斷等）中止時，退避等待恢復後補一則系統
	// 續跑提示、帶完整歷史重跑，直到成功或重試用盡。回合/成本熔斷等【終局】錯誤不重試（重試只會再撞牆）。
	for attempt := 0; ; {
		err := eng.Run(context.Background(), session, reporter)
		if err == nil {
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
func (c *Core) tryResolveApproval(convID, text string) bool {
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
	SendMessage(r.convID, fmt.Sprintf("🛠️ *正在執行工具*：`%s`\n參數：`%s`", toolName, args))
}

func (r *reporter) OnToolResult(ctx context.Context, toolName, result string, isError bool) {
	if isError {
		SendMessage(r.convID, fmt.Sprintf("⚠️ *執行報錯* (%s)：\n%s", toolName, result))
	} else {
		SendMessage(r.convID, fmt.Sprintf("✅ *執行成功* (%s)", toolName))
	}
}

func (r *reporter) OnMessage(ctx context.Context, content string) { SendMessage(r.convID, content) }

// OnTurn 不回推（回合計數對使用者是噪音）。
func (r *reporter) OnTurn(ctx context.Context, turn int) {}

var _ engine.Reporter = (*reporter)(nil)
