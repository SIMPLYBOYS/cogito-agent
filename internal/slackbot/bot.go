// Package slackbot 把 AgentEngine 接入 Slack：
// 通過 Events API 接收消息（webhook），並以 SlackReporter 將執行進度實時回推到會話。
// 對應教材 ch09 的 internal/feishu/bot.go，將飛書替換為 Slack。
package slackbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/engine"
	"github.com/yourname/go-tiny-claw/internal/schema"
)

// EngineFactory 為每個會話動態組裝一個引擎。ch22 用它讓每個頻道掛上"自己專屬的 CostTracker"，
// 各頻道各記各的賬（registry/middleware 無狀態共享，tracker/session 按頻道隔離）。
type EngineFactory func(session *ctxpkg.Session) *engine.AgentEngine

type SlackBot struct {
	client        *slackapi.Client
	signingSecret string
	botUserID     string
	factory       EngineFactory // ch22: 每會話現造引擎（替換原來的固定 engine）
	workDir       string        // 各頻道 session 共用的工作目錄（tools 也註冊在此）

	// 弱點修補②：標記正在運行（含等待審批）的頻道，避免同一 session 併發起第二個 Run
	// 導致懸空 tool_use / 狀態汙染。
	busy   map[string]bool
	busyMu sync.Mutex
}

func NewSlackBot(factory EngineFactory, workDir string) *SlackBot {
	botToken := os.Getenv("SLACK_BOT_TOKEN")
	signingSecret := os.Getenv("SLACK_SIGNING_SECRET")

	if botToken == "" || signingSecret == "" {
		log.Fatal("請設置 SLACK_BOT_TOKEN 和 SLACK_SIGNING_SECRET")
	}

	client := slackapi.New(botToken)

	// 獲取機器人自身的 UserID：用於剝離 @提及 文本、過濾自己發出的消息避免迴環
	authResp, err := client.AuthTest()
	if err != nil {
		log.Fatalf("Slack 鑑權失敗，請檢查 SLACK_BOT_TOKEN: %v", err)
	}

	return &SlackBot{
		client:        client,
		signingSecret: signingSecret,
		botUserID:     authResp.UserID,
		factory:       factory,
		workDir:       workDir,
		busy:          make(map[string]bool),
	}
}

// HandleEvent 是 Slack Events API 的 HTTP 回調入口
func (b *SlackBot) HandleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// 1. 用 Signing Secret 驗證請求確實來自 Slack
	sv, err := slackapi.NewSecretsVerifier(r.Header, b.signingSecret)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if _, err := sv.Write(body); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := sv.Ensure(); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	event, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionNoVerifyToken())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// 2. 首次在 Slack 後臺配置回調地址時，會收到 URL 驗證挑戰，原樣回傳 challenge
	if event.Type == slackevents.URLVerification {
		var challenge slackevents.ChallengeResponse
		if err := json.Unmarshal(body, &challenge); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(challenge.Challenge))
		return
	}

	// 3. 業務事件
	if event.Type == slackevents.CallbackEvent {
		switch ev := event.InnerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			// 頻道里 @機器人
			prompt := strings.TrimSpace(strings.ReplaceAll(ev.Text, fmt.Sprintf("<@%s>", b.botUserID), ""))
			// 先看是不是審批口令（即便會話忙碌也要處理——這正是解開忙碌的方式）
			if b.tryResolveApproval(ev.Channel, prompt) {
				break
			}
			// 弱點修補②：會話忙碌時拒絕新任務，避免併發 Run 汙染同一 session
			if !b.tryAcquire(ev.Channel) {
				b.SendMessage(ev.Channel, "⏳ 上一個任務仍在進行（或正在等待審批），請先處理審批或稍候再發。")
				break
			}
			log.Printf("[Slack] 收到頻道 %s 提及: %s\n", ev.Channel, prompt)
			go b.handleAgentRun(ev.Channel, prompt)

		case *slackevents.MessageEvent:
			// 私聊（DM）消息；過濾機器人消息與編輯/系統子類型，避免迴環
			if ev.BotID != "" || ev.User == b.botUserID || ev.SubType != "" {
				break
			}
			if ev.ChannelType == "im" {
				text := strings.TrimSpace(ev.Text)
				if b.tryResolveApproval(ev.Channel, text) {
					break
				}
				if !b.tryAcquire(ev.Channel) {
					b.SendMessage(ev.Channel, "⏳ 上一個任務仍在進行（或正在等待審批），請先處理審批或稍候再發。")
					break
				}
				log.Printf("[Slack] 收到私聊 %s 消息: %s\n", ev.Channel, ev.Text)
				go b.handleAgentRun(ev.Channel, ev.Text)
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

func (b *SlackBot) handleAgentRun(channelID string, prompt string) {
	defer b.release(channelID) // 弱點修補②：運行結束（含審批被拒/超時/崩潰）釋放忙碌標記

	reporter := &SlackReporter{
		client:    b.client,
		channelID: channelID,
	}

	// 每個 Slack 頻道/私聊 = 一個持久 Session：多輪對話記憶 + 跨頻道隔離，
	// 由 GlobalSessionMgr 按 channelID 管理。
	session := ctxpkg.GlobalSessionMgr.GetOrCreate(channelID, b.workDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	// ch22: 每會話用 factory 現造一個掛了專屬 CostTracker 的引擎，各頻道各記各的賬
	eng := b.factory(session)
	if err := eng.Run(context.Background(), session, reporter); err != nil {
		reporter.sendMsg(fmt.Sprintf("❌ Agent 運行崩潰: %v", err))
	}
}

// SendMessage 向指定頻道發送一條消息（供 ch16 審批 middleware 推送審批請求用）。
func (b *SlackBot) SendMessage(channelID, text string) {
	if _, _, err := b.client.PostMessage(channelID, slackapi.MsgOptionText(text, false)); err != nil {
		log.Printf("[Slack] 消息發送失敗: %v\n", err)
	}
}

// tryResolveApproval 攔截 approve/reject 口令。命中即消費（返回 true，不會被當成新任務）。
// 弱點修補①：支持裸 approve/reject —— 不帶 ID 時按"發起請求的本頻道"解析其待審批項，
// 無需人類手打長長的 taskID。仍兼容 approve <taskID> 的精確形式。
func (b *SlackBot) tryResolveApproval(channelID, text string) bool {
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
		return false // 不是審批口令
	}

	// 取出可能攜帶的 taskID（裸口令則為空）
	idPart := strings.TrimSpace(text[strings.IndexByte(text, ' ')+1:])
	if !strings.ContainsRune(text, ' ') {
		idPart = ""
	}

	if idPart != "" {
		if !GlobalApprovalMgr.ResolveApproval(idPart, allowed, reason) {
			b.SendMessage(channelID, fmt.Sprintf("⚠️ 未找到待審批任務 `%s`（可能已超時或已處理）。", idPart))
		}
		return true
	}

	// 裸口令：解決本頻道所有待審批
	switch n := GlobalApprovalMgr.ResolveByChannel(channelID, allowed, reason); {
	case n == 0:
		b.SendMessage(channelID, "ℹ️ 當前沒有待審批的操作。")
	case n > 1:
		verb := "批准"
		if !allowed {
			verb = "拒絕"
		}
		b.SendMessage(channelID, fmt.Sprintf("已對本頻道 %d 個待審批操作執行%s。", n, verb))
	}
	return true
}

// tryAcquire 標記頻道為忙碌；若已忙碌返回 false（弱點修補②：防同一 session 併發 Run）。
func (b *SlackBot) tryAcquire(channelID string) bool {
	b.busyMu.Lock()
	defer b.busyMu.Unlock()
	if b.busy[channelID] {
		return false
	}
	b.busy[channelID] = true
	return true
}

func (b *SlackBot) release(channelID string) {
	b.busyMu.Lock()
	delete(b.busy, channelID)
	b.busyMu.Unlock()
}

type SlackReporter struct {
	client    *slackapi.Client
	channelID string
}

func (r *SlackReporter) sendMsg(text string) {
	_, _, err := r.client.PostMessage(r.channelID, slackapi.MsgOptionText(text, false))
	if err != nil {
		log.Printf("[Slack] 消息發送失敗: %v\n", err)
	}
}

func (r *SlackReporter) OnThinking(ctx context.Context) {
	r.sendMsg("🤔 模型正在慢思考 (Thinking)...")
}

func (r *SlackReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.sendMsg(fmt.Sprintf("🛠️ *正在執行工具*：`%s`\n參數：`%s`", toolName, args))
}

func (r *SlackReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(fmt.Sprintf("⚠️ *執行報錯* (%s)：\n%s", toolName, result))
	} else {
		r.sendMsg(fmt.Sprintf("✅ *執行成功* (%s)", toolName))
	}
}

func (r *SlackReporter) OnMessage(ctx context.Context, content string) {
	r.sendMsg(content)
}

var _ engine.Reporter = (*SlackReporter)(nil)
