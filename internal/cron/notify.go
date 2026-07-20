package cron

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	slackapi "github.com/slack-go/slack"
)

// 排程結果推播。
//
// 【為何不用 chatbot.SendMessage】那是【行程內】的 router：sender 由 bot 啟動時註冊。dashboard
// 行程沒有任何 sender，呼叫只會靜默無事。排程器現在兩邊都可能跑，與其讓行為隨「誰觸發」而異，
// 不如一律直接拿 token 送——同一條路徑、同樣結果。
//
// 目標格式沿用 chatbot 的 convID 慣例："<平台>:<頻道或聊天室 id>"，例如 slack:C0123ABC。
const (
	// NotifyTargetKey / NotifyErrOnlyKey 匯出給面板的設定表單用（非祕密：只是頻道 id 與開關）。
	NotifyTargetKey  = "COGITO_CRON_NOTIFY"
	NotifyErrOnlyKey = "COGITO_CRON_NOTIFY_ERRORS_ONLY"

	notifyTimeout = 10 * time.Second
	// noticeReplyMax 是摘要裡帶的回覆長度上限——通知是「提醒去看」，不是搬運全文。
	noticeReplyMax = 400
)

// telegramAPIBase 是變數而非常數，測試可指向假 server。
var telegramAPIBase = "https://api.telegram.org/bot"

func NotifyTarget() string   { return strings.TrimSpace(os.Getenv(NotifyTargetKey)) }
func NotifyErrorsOnly() bool { return os.Getenv(NotifyErrOnlyKey) == "1" }

// ShouldNotify：沒設目標就不送；設了「只送失敗」則成功時不吵。
func ShouldNotify(target, status string) bool {
	if target == "" {
		return false
	}
	return !(NotifyErrorsOnly() && status == "ok")
}

// buildNotice 組通知文字（純函式，好測）。回覆過長截斷——要看全文去 /runs。
func buildNotice(j Job, status, errMsg, reply string, dur time.Duration) string {
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
		b.WriteString(r)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "執行樹：/runs/%s", SessionID(j.ID))
	return b.String()
}

// parseTarget 拆解並驗證推播目標。
//
// 【為何連 token 形狀都擋】這欄填的是【收件地址】且會明碼顯示在 cron 頁標題上；誤把 bot token
// 貼進來等於把憑證印在畫面、並寫進 .env 的非祕密欄位。token 該放「金鑰／祕密」區。
func parseTarget(target string) (plat, id string, err error) {
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

// SplitTargets 拆逗號分隔的多目標（去空白、略過空項），支援同時送 Slack 與 Telegram。
func SplitTargets(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ValidateTargets 逐一驗證（存檔時用）：任一個不合法就整批擋下，避免存進一半能用的設定。
func ValidateTargets(s string) error {
	for _, t := range SplitTargets(s) {
		if _, _, err := parseTarget(t); err != nil {
			return fmt.Errorf("%q %v", t, err)
		}
	}
	return nil
}

// SendAll 送到所有目標。單一目標失敗不影響其他（Slack 掛了不該讓 Telegram 也收不到），
// 最後把各自的錯誤合併回報。
func SendAll(targets, text string) error {
	var errs []string
	for _, t := range SplitTargets(targets) {
		if err := sendOne(t, text); err != nil {
			errs = append(errs, fmt.Sprintf("%s → %v", t, err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "；"))
	}
	return nil
}

// sendOne 送單一目標，依平台分派。
func sendOne(target, text string) error {
	plat, id, err := parseTarget(target)
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
