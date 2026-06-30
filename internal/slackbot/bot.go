// Package slackbot 是 chatbot.Core 的 Slack 傳輸層：Events API webhook 入站解析 + 簽章驗證 +
// @提及剝離，把清理後的 (頻道, 文本) 交給共用核心；發送走 Slack Web API。指令閘/會話隔離/鎖/
// 跑任務管線都在 internal/chatbot（與平台無關，Telegram 等共用）。
package slackbot

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// platform 是本傳輸層的命名空間前綴（見 chatbot.Core）。
const platform = "slack"

type SlackBot struct {
	core          *chatbot.Core
	client        *slackapi.Client
	signingSecret string
	botUserID     string
	mention       *regexp.Regexp // 剝除 @提及（含 <@ID> 與 <@ID|顯示名> 兩種格式）
}

func NewSlackBot(factory chatbot.EngineFactory, workDir string) *SlackBot {
	botToken := os.Getenv("SLACK_BOT_TOKEN")
	signingSecret := os.Getenv("SLACK_SIGNING_SECRET")
	if botToken == "" || signingSecret == "" {
		log.Fatal("請設置 SLACK_BOT_TOKEN 和 SLACK_SIGNING_SECRET")
	}

	client := slackapi.New(botToken)
	authResp, err := client.AuthTest() // 取自身 UserID：剝離 @提及、過濾自己發出的消息避免迴環
	if err != nil {
		log.Fatalf("Slack 鑑權失敗，請檢查 SLACK_BOT_TOKEN: %v", err)
	}

	send := func(channelID, text string) {
		if _, _, err := client.PostMessage(channelID, slackapi.MsgOptionText(text, false)); err != nil {
			log.Printf("[Slack] 消息發送失敗: %v\n", err)
		}
	}
	return &SlackBot{
		core:          chatbot.NewCore(platform, workDir, factory, send),
		client:        client,
		signingSecret: signingSecret,
		botUserID:     authResp.UserID,
		mention:       botMentionRegexp(authResp.UserID),
	}
}

// botMentionRegexp 匹配本 bot 的 @提及，兩種格式都吃：<@U123> 與 <@U123|顯示名>。
func botMentionRegexp(botID string) *regexp.Regexp {
	return regexp.MustCompile("<@" + regexp.QuoteMeta(botID) + `(\|[^>]*)?>`)
}

func (b *SlackBot) SetPostRunHook(h chatbot.PostRunHook)         { b.core.SetPostRunHook(h) }
func (b *SlackBot) SetPostFailureHook(h chatbot.PostFailureHook) { b.core.SetPostFailureHook(h) }

// SendMessage 以命名空間 convID（"slack:頻道"）路由發送，供 cmd 的審批/提案通知用。
func (b *SlackBot) SendMessage(convID, text string) { chatbot.SendMessage(convID, text) }

// HandleEvent 是 Slack Events API 的 HTTP 回調入口：驗簽 → 解析 → 交給 core.Dispatch。
func (b *SlackBot) HandleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

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

	// 首次配置回調地址時，Slack 發來 URL 驗證挑戰，原樣回傳 challenge。
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

	if event.Type == slackevents.CallbackEvent {
		switch ev := event.InnerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			prompt := strings.TrimSpace(b.mention.ReplaceAllString(ev.Text, ""))
			b.core.Dispatch(ev.Channel, prompt)
		case *slackevents.MessageEvent:
			// 私聊（DM）；過濾機器人消息與編輯/系統子類型，避免迴環。
			if ev.BotID != "" || ev.User == b.botUserID || ev.SubType != "" {
				break
			}
			if ev.ChannelType == "im" {
				b.core.Dispatch(ev.Channel, strings.TrimSpace(ev.Text))
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}
