// Package telegrambot 是 chatbot.Core 的 Telegram 傳輸層：用 getUpdates 長輪詢收訊（零基建，
// 不需公開 URL／webhook），把 (chat, 文本) 交給共用核心；發送走 Bot API sendMessage。
// 指令閘/會話隔離/鎖/跑任務管線都在 internal/chatbot，與 Slack 共用同一套。
package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
)

const (
	platform       = "telegram"
	apiBase        = "https://api.telegram.org/bot"
	pollTimeoutSec = 50 // 長輪詢秒數；client.Timeout 須略大於它
)

type TelegramBot struct {
	core   *chatbot.Core
	token  string
	client *http.Client
}

func NewTelegramBot(factory chatbot.EngineFactory, workDir string) *TelegramBot {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("請設置 TELEGRAM_BOT_TOKEN")
	}
	b := &TelegramBot{token: token, client: &http.Client{Timeout: (pollTimeoutSec + 15) * time.Second}}
	b.core = chatbot.NewCore(platform, workDir, factory, b.send)
	return b
}

func (b *TelegramBot) SetPostRunHook(h chatbot.PostRunHook)         { b.core.SetPostRunHook(h) }
func (b *TelegramBot) SetPostFailureHook(h chatbot.PostFailureHook) { b.core.SetPostFailureHook(h) }

// SendMessage 以命名空間 convID（"telegram:chatID"）路由發送，供 cmd 的審批/提案通知用。
func (b *TelegramBot) SendMessage(convID, text string) { chatbot.SendMessage(convID, text) }

// send 是核心注入的原生發送：chatID 為純數字字串，Bot API 的 chat_id 要整數。
func (b *TelegramBot) send(chatID, text string) {
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		log.Printf("[Telegram] 非法 chat_id %q: %v\n", chatID, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{"chat_id": id, "text": text})
	resp, err := b.client.Post(apiBase+b.token+"/sendMessage", "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[Telegram] 消息發送失敗: %v\n", err)
		return
	}
	resp.Body.Close()
}

// Start 阻塞跑長輪詢迴圈，直到 ctx 取消。網路出錯退避重試，不讓整個 bot 倒下。
func (b *TelegramBot) Start(ctx context.Context) {
	log.Printf("🚀 cogito-agent Telegram 服務已啟動（getUpdates 長輪詢）")
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[Telegram] getUpdates 出錯，3 秒後重試: %v\n", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1 // 確認到此，下次只取更新的
			m := u.Message
			if m == nil || m.From == nil || m.From.IsBot {
				continue
			}
			text := strings.TrimSpace(m.Text)
			if text == "" || text == "/start" { // /start 是 Telegram 慣例的開場，不當任務
				if text == "/start" {
					b.send(strconv.FormatInt(m.Chat.ID, 10), "👋 我是 cogito-agent。直接打字交辦任務即可；危險操作會請你回覆 approve/reject。")
				}
				continue
			}
			b.core.Dispatch(strconv.FormatInt(m.Chat.ID, 10), text)
		}
	}
}

func (b *TelegramBot) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	url := fmt.Sprintf("%s%s/getUpdates?timeout=%d&offset=%d", apiBase, b.token, pollTimeoutSec, offset)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return parseUpdates(resp.Body)
}

// update 只取我們用得到的欄位（Telegram 回應遠不止這些）。
type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From *struct {
			IsBot bool `json:"is_bot"`
		} `json:"from"`
	} `json:"message"`
}

func parseUpdates(r io.Reader) ([]update, error) {
	var body struct {
		OK     bool     `json:"ok"`
		Result []update `json:"result"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	return body.Result, nil
}
