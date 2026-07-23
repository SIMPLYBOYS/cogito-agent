// Package slackbot 是 chatbot.Core 的 Slack 傳輸層：走 Socket Mode（outbound websocket，免公開
// URL／webhook／ngrok）收 Events API 事件，剝離 @提及後把 (頻道, 文本) 交給共用核心；發送走
// Slack Web API。指令閘/會話隔離/鎖/跑任務管線都在 internal/chatbot，與 Telegram 共用同一套。
package slackbot

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// platform 是本傳輸層的命名空間前綴（見 chatbot.Core）。
const platform = "slack"

type SlackBot struct {
	core      *chatbot.Core
	client    *socketmode.Client
	botUserID string
	mention   *regexp.Regexp // 剝除 @提及（含 <@ID> 與 <@ID|顯示名> 兩種格式）
}

func NewSlackBot(factory chatbot.EngineFactory, workDir string) *SlackBot {
	botToken := os.Getenv("SLACK_BOT_TOKEN")
	appToken := os.Getenv("SLACK_APP_TOKEN")
	if botToken == "" || appToken == "" {
		log.Fatal("請設置 SLACK_BOT_TOKEN（xoxb-）與 SLACK_APP_TOKEN（xapp-，App 後臺啟用 Socket Mode 後取得）")
	}

	api := slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	authResp, err := api.AuthTest() // 取自身 UserID：剝離 @提及、過濾自己發出的消息避免迴環
	if err != nil {
		log.Fatalf("Slack 鑑權失敗，請檢查 SLACK_BOT_TOKEN: %v", err)
	}

	send := func(channelID, text string) {
		// Slack mrkdwn 原生吃 ``` 等寬 block（表格對齊靠它），但粗體是單星號：**x**→*x*。
		if _, _, err := api.PostMessage(channelID, slackapi.MsgOptionText(slackMrkdwn(text), false)); err != nil {
			log.Printf("[Slack] 消息發送失敗: %v\n", err)
		}
	}
	// `get` 檔案取回（user-pull）：走 files 三段式上傳（getUploadURLExternal → POST → complete）。
	chatbot.RegisterFileSender(platform, func(channelID, path string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		_, err = api.UploadFile(slackapi.UploadFileParameters{
			Reader:   f,
			FileSize: int(fi.Size()),
			Filename: filepath.Base(path),
			Channel:  channelID,
		})
		return err
	})
	return &SlackBot{
		core:      chatbot.NewCore(platform, workDir, factory, send),
		client:    socketmode.New(api),
		botUserID: authResp.UserID,
		mention:   botMentionRegexp(authResp.UserID),
	}
}

// botMentionRegexp 匹配本 bot 的 @提及，兩種格式都吃：<@U123> 與 <@U123|顯示名>。
func botMentionRegexp(botID string) *regexp.Regexp {
	return regexp.MustCompile("<@" + regexp.QuoteMeta(botID) + `(\|[^>]*)?>`)
}

var slackBoldRe = regexp.MustCompile(`\*\*(.+?)\*\*`)

// slackMrkdwn 把中介格式的 **粗體** 轉成 Slack 的單星號 *粗體*（``` 等寬 block Slack 原生就 render）。
func slackMrkdwn(s string) string {
	return slackBoldRe.ReplaceAllString(s, "*$1*")
}

func (b *SlackBot) SetPostRunHook(h chatbot.PostRunHook)         { b.core.SetPostRunHook(h) }
func (b *SlackBot) SetPostFailureHook(h chatbot.PostFailureHook) { b.core.SetPostFailureHook(h) }
func (b *SlackBot) SetLearnHook(h chatbot.LearnHook)             { b.core.SetLearnHook(h) }

// SendMessage 以命名空間 convID（"slack:頻道"）路由發送，供 cmd 的審批/提案通知用。
func (b *SlackBot) SendMessage(convID, text string) { chatbot.SendMessage(convID, text) }

// ResumeInterrupted 啟動時自動續跑被硬砍中斷的任務（見 chatbot.Core.ResumeInterrupted）。
func (b *SlackBot) ResumeInterrupted() { b.core.ResumeInterrupted() }

// Start 阻塞跑 Socket Mode：開一條到 Slack 的 websocket 收事件，直到 ctx 取消。事件處理在
// 另一 goroutine 消費 client.Events，每筆 Events API 事件必須 Ack 否則 Slack 會重送。
func (b *SlackBot) Start(ctx context.Context) {
	log.Printf("🚀 cogito-agent Slack 服務已啟動（Socket Mode，outbound websocket，免公開 URL）")
	go func() {
		for evt := range b.client.Events {
			switch evt.Type {
			case socketmode.EventTypeConnecting:
				log.Printf("[Slack] Socket Mode 連線中…")
				continue
			case socketmode.EventTypeConnected:
				log.Printf("[Slack] Socket Mode 已連線 ✅（開始接收事件）")
				continue
			case socketmode.EventTypeConnectionError:
				log.Printf("[Slack] Socket Mode 連線錯誤: %v（檢查 SLACK_APP_TOKEN 與其 connections:write scope）", evt.Data)
				continue
			case socketmode.EventTypeEventsAPI:
				// 往下處理
			default:
				continue // hello / 心跳 / 互動 等無需處理
			}
			apiEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}
			b.client.Ack(*evt.Request) // 先 Ack，避免 Slack 視為失敗而重送
			log.Printf("[Slack] 收到 Events API 事件: %s", apiEvent.InnerEvent.Type)
			if apiEvent.Type != slackevents.CallbackEvent {
				continue
			}
			switch ev := apiEvent.InnerEvent.Data.(type) {
			case *slackevents.AppMentionEvent:
				prompt := strings.TrimSpace(b.mention.ReplaceAllString(ev.Text, ""))
				b.core.Dispatch(ev.Channel, ev.User, prompt)
			case *slackevents.MessageEvent:
				// 私聊（DM）；過濾機器人消息與編輯/系統子類型，避免迴環。
				if ev.BotID != "" || ev.User == b.botUserID || ev.SubType != "" {
					continue
				}
				if ev.ChannelType == "im" {
					b.core.DispatchDM(ev.Channel, ev.User, strings.TrimSpace(ev.Text))
				}
			}
		}
	}()
	if err := b.client.RunContext(ctx); err != nil && ctx.Err() == nil {
		log.Printf("[Slack] Socket Mode 連線結束: %v\n", err)
	}
}
