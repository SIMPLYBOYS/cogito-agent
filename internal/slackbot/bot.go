// Package slackbot 把 AgentEngine 接入 Slack：
// 通过 Events API 接收消息（webhook），并以 SlackReporter 将执行进度实时回推到会话。
// 对应教材 ch09 的 internal/feishu/bot.go，将飞书替换为 Slack。
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

// EngineFactory 为每个会话动态组装一个引擎。ch22 用它让每个频道挂上"自己专属的 CostTracker"，
// 各频道各记各的账（registry/middleware 无状态共享，tracker/session 按频道隔离）。
type EngineFactory func(session *ctxpkg.Session) *engine.AgentEngine

type SlackBot struct {
	client        *slackapi.Client
	signingSecret string
	botUserID     string
	factory       EngineFactory // ch22: 每会话现造引擎（替换原来的固定 engine）
	workDir       string        // 各频道 session 共用的工作目录（tools 也注册在此）

	// 弱点修补②：标记正在运行（含等待审批）的频道，避免同一 session 并发起第二个 Run
	// 导致悬空 tool_use / 状态污染。
	busy   map[string]bool
	busyMu sync.Mutex
}

func NewSlackBot(factory EngineFactory, workDir string) *SlackBot {
	botToken := os.Getenv("SLACK_BOT_TOKEN")
	signingSecret := os.Getenv("SLACK_SIGNING_SECRET")

	if botToken == "" || signingSecret == "" {
		log.Fatal("请设置 SLACK_BOT_TOKEN 和 SLACK_SIGNING_SECRET")
	}

	client := slackapi.New(botToken)

	// 获取机器人自身的 UserID：用于剥离 @提及 文本、过滤自己发出的消息避免回环
	authResp, err := client.AuthTest()
	if err != nil {
		log.Fatalf("Slack 鉴权失败，请检查 SLACK_BOT_TOKEN: %v", err)
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

// HandleEvent 是 Slack Events API 的 HTTP 回调入口
func (b *SlackBot) HandleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// 1. 用 Signing Secret 验证请求确实来自 Slack
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

	// 2. 首次在 Slack 后台配置回调地址时，会收到 URL 验证挑战，原样回传 challenge
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

	// 3. 业务事件
	if event.Type == slackevents.CallbackEvent {
		switch ev := event.InnerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			// 频道里 @机器人
			prompt := strings.TrimSpace(strings.ReplaceAll(ev.Text, fmt.Sprintf("<@%s>", b.botUserID), ""))
			// 先看是不是审批口令（即便会话忙碌也要处理——这正是解开忙碌的方式）
			if b.tryResolveApproval(ev.Channel, prompt) {
				break
			}
			// 弱点修补②：会话忙碌时拒绝新任务，避免并发 Run 污染同一 session
			if !b.tryAcquire(ev.Channel) {
				b.SendMessage(ev.Channel, "⏳ 上一个任务仍在进行（或正在等待审批），请先处理审批或稍候再发。")
				break
			}
			log.Printf("[Slack] 收到频道 %s 提及: %s\n", ev.Channel, prompt)
			go b.handleAgentRun(ev.Channel, prompt)

		case *slackevents.MessageEvent:
			// 私聊（DM）消息；过滤机器人消息与编辑/系统子类型，避免回环
			if ev.BotID != "" || ev.User == b.botUserID || ev.SubType != "" {
				break
			}
			if ev.ChannelType == "im" {
				text := strings.TrimSpace(ev.Text)
				if b.tryResolveApproval(ev.Channel, text) {
					break
				}
				if !b.tryAcquire(ev.Channel) {
					b.SendMessage(ev.Channel, "⏳ 上一个任务仍在进行（或正在等待审批），请先处理审批或稍候再发。")
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
	defer b.release(channelID) // 弱点修补②：运行结束（含审批被拒/超时/崩溃）释放忙碌标记

	reporter := &SlackReporter{
		client:    b.client,
		channelID: channelID,
	}

	// 每个 Slack 频道/私聊 = 一个持久 Session：多轮对话记忆 + 跨频道隔离，
	// 由 GlobalSessionMgr 按 channelID 管理。
	session := ctxpkg.GlobalSessionMgr.GetOrCreate(channelID, b.workDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	// ch22: 每会话用 factory 现造一个挂了专属 CostTracker 的引擎，各频道各记各的账
	eng := b.factory(session)
	if err := eng.Run(context.Background(), session, reporter); err != nil {
		reporter.sendMsg(fmt.Sprintf("❌ Agent 运行崩溃: %v", err))
	}
}

// SendMessage 向指定频道发送一条消息（供 ch16 审批 middleware 推送审批请求用）。
func (b *SlackBot) SendMessage(channelID, text string) {
	if _, _, err := b.client.PostMessage(channelID, slackapi.MsgOptionText(text, false)); err != nil {
		log.Printf("[Slack] 消息发送失败: %v\n", err)
	}
}

// tryResolveApproval 拦截 approve/reject 口令。命中即消费（返回 true，不会被当成新任务）。
// 弱点修补①：支持裸 approve/reject —— 不带 ID 时按"发起请求的本频道"解析其待审批项，
// 无需人类手打长长的 taskID。仍兼容 approve <taskID> 的精确形式。
func (b *SlackBot) tryResolveApproval(channelID, text string) bool {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)

	var allowed bool
	var reason string
	switch {
	case lower == "approve" || strings.HasPrefix(lower, "approve "):
		allowed, reason = true, "人类管理员已批准操作"
	case lower == "reject" || strings.HasPrefix(lower, "reject "):
		allowed, reason = false, "人类管理员认为风险过高，已拒绝该操作"
	default:
		return false // 不是审批口令
	}

	// 取出可能携带的 taskID（裸口令则为空）
	idPart := strings.TrimSpace(text[strings.IndexByte(text, ' ')+1:])
	if !strings.ContainsRune(text, ' ') {
		idPart = ""
	}

	if idPart != "" {
		if !GlobalApprovalMgr.ResolveApproval(idPart, allowed, reason) {
			b.SendMessage(channelID, fmt.Sprintf("⚠️ 未找到待审批任务 `%s`（可能已超时或已处理）。", idPart))
		}
		return true
	}

	// 裸口令：解决本频道所有待审批
	switch n := GlobalApprovalMgr.ResolveByChannel(channelID, allowed, reason); {
	case n == 0:
		b.SendMessage(channelID, "ℹ️ 当前没有待审批的操作。")
	case n > 1:
		verb := "批准"
		if !allowed {
			verb = "拒绝"
		}
		b.SendMessage(channelID, fmt.Sprintf("已对本频道 %d 个待审批操作执行%s。", n, verb))
	}
	return true
}

// tryAcquire 标记频道为忙碌；若已忙碌返回 false（弱点修补②：防同一 session 并发 Run）。
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
		log.Printf("[Slack] 消息发送失败: %v\n", err)
	}
}

func (r *SlackReporter) OnThinking(ctx context.Context) {
	r.sendMsg("🤔 模型正在慢思考 (Thinking)...")
}

func (r *SlackReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.sendMsg(fmt.Sprintf("🛠️ *正在执行工具*：`%s`\n参数：`%s`", toolName, args))
}

func (r *SlackReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(fmt.Sprintf("⚠️ *执行报错* (%s)：\n%s", toolName, result))
	} else {
		r.sendMsg(fmt.Sprintf("✅ *执行成功* (%s)", toolName))
	}
}

func (r *SlackReporter) OnMessage(ctx context.Context, content string) {
	r.sendMsg(content)
}

var _ engine.Reporter = (*SlackReporter)(nil)
