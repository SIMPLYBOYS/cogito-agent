package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	slackapi "github.com/slack-go/slack"
)

// 排程結果推播。
//
// 【為何不用 chatbot.SendMessage】那是【行程內】的 router：sender 由 bot 啟動時註冊進 chatbot.senders。
// dashboard 是另一個行程、沒有任何 sender 註冊，呼叫它只會靜默無事。所以這裡直接拿 token 送。
//
// 目標格式沿用 chatbot 的 convID 慣例："<平台>:<頻道或聊天室 id>"，例如 slack:C0123ABC、telegram:12345678。
const (
	notifyTargetKey  = "COGITO_CRON_NOTIFY"
	notifyErrOnlyKey = "COGITO_CRON_NOTIFY_ERRORS_ONLY"
	notifyTimeout    = 10 * time.Second
	// noticeReplyMax 是摘要裡帶的回覆長度上限——通知是「提醒去看」，不是搬運全文。
	noticeReplyMax = 400
)

// telegramAPIBase 是變數而非常數，測試可指向假 server。
var telegramAPIBase = "https://api.telegram.org/bot"

func cronNotifyTarget() string   { return strings.TrimSpace(os.Getenv(notifyTargetKey)) }
func cronNotifyErrorsOnly() bool { return os.Getenv(notifyErrOnlyKey) == "1" }

// shouldNotify：沒設目標就不送；設了「只送失敗」則成功時不吵。
func shouldNotify(target, status string) bool {
	if target == "" {
		return false
	}
	return !(cronNotifyErrorsOnly() && status == "ok")
}

// buildCronNotice 組通知文字（純函式，好測）。回覆過長截斷——要看全文去 /runs。
func buildCronNotice(j cronJob, status, errMsg, reply string, dur time.Duration) string {
	icon := "✅"
	if status != "ok" {
		icon = "❌"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s cron「%s」%s（%s，耗時 %s）\n", icon, j.Name, status, j.Schedule, dur.Round(time.Second))
	if errMsg != "" {
		fmt.Fprintf(&b, "錯誤：%s\n", errMsg)
	}
	if r := strings.TrimSpace(reply); r != "" {
		if len([]rune(r)) > noticeReplyMax {
			r = string([]rune(r)[:noticeReplyMax]) + "…（截斷）"
		}
		b.WriteString(r + "\n")
	}
	fmt.Fprintf(&b, "執行樹：/runs/%s", cronSessionID(j.ID))
	return b.String()
}

// parseNotifyTarget 拆解並驗證推播目標。
//
// 【為何連 token 形狀都擋】這欄填的是【收件地址】且會明碼顯示在 cron 頁標題上；誤把 bot token
// 貼進來等於把憑證印在畫面、並寫進 .env 的非祕密欄位。token 該放「金鑰／祕密」區。
func parseNotifyTarget(target string) (plat, id string, err error) {
	p, i, ok := strings.Cut(strings.TrimSpace(target), ":")
	if !ok || strings.TrimSpace(i) == "" {
		return "", "", fmt.Errorf("格式須為 <平台>:<id>（如 slack:C0123ABC），實得 %q", target)
	}
	plat, id = strings.ToLower(strings.TrimSpace(p)), strings.TrimSpace(i)
	if plat != "slack" && plat != "telegram" {
		return "", "", fmt.Errorf("不支援的通知平台 %q（目前支援 slack / telegram）", p)
	}
	if looksLikeToken(id) {
		return "", "", fmt.Errorf("這裡要填【收件頻道／聊天室 id】（如 C0123ABC），不是 token；" +
			"token 請設在 platform 的「金鑰／祕密」區")
	}
	return plat, id, nil
}

// looksLikeToken 粗判誤貼的憑證：Slack 一律 xox*／xapp- 開頭；頻道與聊天室 id 都很短，
// 過長幾乎必然是 token。寧可誤擋也不要讓憑證明碼上畫面。
func looksLikeToken(s string) bool {
	low := strings.ToLower(s)
	return strings.HasPrefix(low, "xox") || strings.HasPrefix(low, "xapp-") ||
		strings.HasPrefix(low, "bot") || len(s) > 40
}

// sendNotify 依目標平台分派。回錯由呼叫端記 log——通知失敗不該影響排程本身。
func sendNotify(target, text string) error {
	plat, id, err := parseNotifyTarget(target)
	if err != nil {
		return err
	}
	if plat == "slack" {
		return sendSlack(id, text)
	}
	return sendTelegram(id, text)
}

func sendSlack(channel, text string) error {
	token := strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN"))
	if token == "" {
		return fmt.Errorf("未設 SLACK_BOT_TOKEN")
	}
	_, _, err := slackapi.New(token).PostMessage(channel, slackapi.MsgOptionText(text, false))
	return err
}

func sendTelegram(chatID, text string) error {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		return fmt.Errorf("未設 TELEGRAM_BOT_TOKEN")
	}
	payload, _ := json.Marshal(map[string]string{"chat_id": chatID, "text": text})
	client := &http.Client{Timeout: notifyTimeout}
	resp, err := client.Post(telegramAPIBase+token+"/sendMessage", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram sendMessage 回 %s", resp.Status)
	}
	return nil
}
