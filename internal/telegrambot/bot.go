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
	"regexp"
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
	core    *chatbot.Core
	token   string
	client  *http.Client
	botID   int64          // 本 bot 的 user id（判斷「回覆到我」用）
	mention *regexp.Regexp // 剝除群組裡的 @提及（如 @cogito_bot，不分大小寫）
}

func NewTelegramBot(factory chatbot.EngineFactory, workDir string) *TelegramBot {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("請設置 TELEGRAM_BOT_TOKEN")
	}
	b := &TelegramBot{token: token, client: &http.Client{Timeout: (pollTimeoutSec + 15) * time.Second}}
	// 取自身 id/username：群組裡用來判斷「有沒有在叫我」並剝掉 @提及（對齊 Slack 的 AuthTest）。
	if err := b.fetchIdentity(); err != nil {
		log.Fatalf("Telegram getMe 失敗，請檢查 TELEGRAM_BOT_TOKEN: %v", err)
	}
	b.core = chatbot.NewCore(platform, workDir, factory, b.send)
	return b
}

// fetchIdentity 以 getMe 取本 bot 的 id 與 username，並編好剝提及的 regexp。
func (b *TelegramBot) fetchIdentity() error {
	resp, err := b.client.Get(apiBase + b.token + "/getMe")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK || r.Result.Username == "" {
		return fmt.Errorf("getMe 回應異常（ok=%v username=%q）", r.OK, r.Result.Username)
	}
	b.botID = r.Result.ID
	b.mention = regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(r.Result.Username))
	return nil
}

func (b *TelegramBot) SetPostRunHook(h chatbot.PostRunHook)         { b.core.SetPostRunHook(h) }
func (b *TelegramBot) SetPostFailureHook(h chatbot.PostFailureHook) { b.core.SetPostFailureHook(h) }

// SendMessage 以命名空間 convID（"telegram:chatID"）路由發送，供 cmd 的審批/提案通知用。
func (b *TelegramBot) SendMessage(convID, text string) { chatbot.SendMessage(convID, text) }

// ResumeInterrupted 啟動時自動續跑被硬砍中斷的任務（見 chatbot.Core.ResumeInterrupted）。
func (b *TelegramBot) ResumeInterrupted() { b.core.ResumeInterrupted() }

// send 是核心注入的原生發送：chatID 為純數字字串，Bot API 的 chat_id 要整數。
// 先以 HTML parse_mode 送（表格→<pre> 等寬對齊、**粗體**→<b>）；若 HTML 不合法（如標記不平衡）
// Telegram 會回非 2xx，此時退回純文字重送一次，確保訊息不因格式化而整條發不出去。
func (b *TelegramBot) send(chatID, text string) {
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		log.Printf("[Telegram] 非法 chat_id %q: %v\n", chatID, err)
		return
	}
	if b.postMessage(id, telegramHTML(text), "HTML") {
		return
	}
	b.postMessage(id, text, "") // fallback：純文字
}

// postMessage 送一則訊息，回傳是否成功（HTTP 2xx）。parseMode 空字串＝純文字。
func (b *TelegramBot) postMessage(id int64, text, parseMode string) bool {
	m := map[string]any{"chat_id": id, "text": text}
	if parseMode != "" {
		m["parse_mode"] = parseMode
	}
	payload, _ := json.Marshal(m)
	resp, err := b.client.Post(apiBase+b.token+"/sendMessage", "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[Telegram] 消息發送失敗: %v\n", err)
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

var (
	tgFenceRe = regexp.MustCompile("(?s)```[a-zA-Z]*\n?(.*?)```")
	tgBoldRe  = regexp.MustCompile(`\*\*(.+?)\*\*`)
	tgCodeRe  = regexp.MustCompile("`([^`\n]+)`")
)

// telegramHTML 把中介格式轉成 Telegram HTML parse_mode：先 escape &<>（純文字/進度訊息在 HTML
// 模式下才安全），再把 ``` 圍欄→<pre>（等寬、保住表格對齊）、**粗體**→<b>、`code`→<code>。
func telegramHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = tgFenceRe.ReplaceAllString(s, "<pre>$1</pre>")
	s = tgBoldRe.ReplaceAllString(s, "<b>$1</b>")
	s = tgCodeRe.ReplaceAllString(s, "<code>$1</code>")
	return s
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
			// DM：每則都當任務。群組：只在 @我 或 回覆到我 時才當任務，並剝掉 @提及。
			prompt, ok := b.addressedText(m)
			if !ok {
				continue // 群組閒聊、沒叫到我 → 不理
			}
			if prompt == "/start" { // Telegram 慣例的開場，不當任務
				b.send(strconv.FormatInt(m.Chat.ID, 10), "👋 我是 cogito-agent。直接打字交辦任務即可（群組裡請 @我 或回覆我）；危險操作會請你回覆 approve/reject。")
				continue
			}
			b.core.Dispatch(strconv.FormatInt(m.Chat.ID, 10), strconv.FormatInt(m.From.ID, 10), prompt)
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

// addressedText 決定一則訊息是否要當任務、以及清理後的 prompt：
//   - 私聊（DM）：每則都算，回原文。
//   - 群組：只在「@本 bot」或「回覆到本 bot 的訊息」時才算，並剝掉 @提及。
//
// 回傳 (prompt, ok)；ok=false 表示忽略（群組閒聊、或剝完變空）。
func (b *TelegramBot) addressedText(m *tgMessage) (string, bool) {
	text := strings.TrimSpace(m.Text)
	if m.Chat.Type == "private" {
		return text, text != ""
	}
	// 群組/超級群組：必須有叫到我。
	repliedToMe := m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && m.ReplyToMessage.From.ID == b.botID
	if !b.mention.MatchString(text) && !repliedToMe {
		return "", false
	}
	text = strings.TrimSpace(b.mention.ReplaceAllString(text, "")) // 剝掉 @cogito_bot，留乾淨任務
	return text, text != ""
}

// Telegram 回應遠不止這些欄位——只取我們用得到的。用具名型別以便帶 chat.type 與 reply_to_message。
type update struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	Text           string     `json:"text"`
	Chat           tgChat     `json:"chat"`
	From           *tgUser    `json:"from"`
	ReplyToMessage *tgMessage `json:"reply_to_message"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private / group / supergroup / channel
}

type tgUser struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}

func parseUpdates(r io.Reader) ([]update, error) {
	var body struct {
		OK          bool     `json:"ok"`
		Description string   `json:"description"`
		Result      []update `json:"result"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	// ok:false 是 Telegram API 的錯誤形式（如 token 失效、被限流）。原本靜默當空更新，
	// 錯誤被吞掉；改回報 error → Start 記日誌並退避重試，而非誤以為「沒新訊息」。
	if !body.OK {
		return nil, fmt.Errorf("Telegram API 回應 ok=false: %s", body.Description)
	}
	return body.Result, nil
}
