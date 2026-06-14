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

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/engine"
	"github.com/yourname/go-tiny-claw/internal/schema"
)

type SlackBot struct {
	client        *slackapi.Client
	signingSecret string
	botUserID     string
	engine        *engine.AgentEngine
	workDir       string // 各频道 session 共用的工作目录（tools 也注册在此）
}

func NewSlackBot(eng *engine.AgentEngine, workDir string) *SlackBot {
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
		engine:        eng,
		workDir:       workDir,
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
			// ch16: 先看是不是审批口令；命中则唤醒对应审批，不当作新任务
			if tryResolveApproval(prompt) {
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
				if tryResolveApproval(strings.TrimSpace(ev.Text)) {
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
	reporter := &SlackReporter{
		client:    b.client,
		channelID: channelID,
	}

	// 每个 Slack 频道/私聊 = 一个持久 Session：多轮对话记忆 + 跨频道隔离，
	// 由 GlobalSessionMgr 按 channelID 管理。
	session := ctxpkg.GlobalSessionMgr.GetOrCreate(channelID, b.workDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := b.engine.Run(context.Background(), session, reporter); err != nil {
		reporter.sendMsg(fmt.Sprintf("❌ Agent 运行崩溃: %v", err))
	}
}

// SendMessage 向指定频道发送一条消息（供 ch16 审批 middleware 推送审批请求用）。
func (b *SlackBot) SendMessage(channelID, text string) {
	if _, _, err := b.client.PostMessage(channelID, slackapi.MsgOptionText(text, false)); err != nil {
		log.Printf("[Slack] 消息发送失败: %v\n", err)
	}
}

// tryResolveApproval 拦截 approve/reject 口令，命中则唤醒对应审批并返回 true。
func tryResolveApproval(text string) bool {
	if rest, ok := strings.CutPrefix(text, "approve "); ok {
		GlobalApprovalMgr.ResolveApproval(strings.TrimSpace(rest), true, "人类管理员已批准操作")
		return true
	}
	if rest, ok := strings.CutPrefix(text, "reject "); ok {
		GlobalApprovalMgr.ResolveApproval(strings.TrimSpace(rest), false, "人类管理员认为风险过高，已拒绝该操作")
		return true
	}
	return false
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
