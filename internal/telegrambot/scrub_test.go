package telegrambot

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// Telegram 把 token 放在 URL path（人家的 API 設計），而 http.Client 失敗回的 *url.Error 帶完整 URL。
// 任何一次網路抖動都會讓 log.Printf("%v", err) 把 token 印出去——而 getUpdates 是每 3 秒重試的無限
// 迴圈，一段網路中斷就能把 token 刷滿 log。這條釘死「錯誤訊息永遠不得含 token」。
func TestScrubErr_RemovesToken(t *testing.T) {
	const token = "123456:AAHfakeTokenDoNotUse"
	b := &TelegramBot{token: token}

	// 真實形狀：http.Client.Get 失敗時就是這個
	realErr := &url.Error{
		Op:  "Get",
		URL: apiBase + token + "/getUpdates?timeout=50&offset=1",
		Err: errors.New("dial tcp: i/o timeout"),
	}
	got := b.scrubErr(realErr)
	if strings.Contains(got, token) {
		t.Errorf("token 洩漏進錯誤訊息: %s", got)
	}
	if !strings.Contains(got, "i/o timeout") {
		t.Errorf("遮蔽後應保留診斷資訊，got: %s", got)
	}

	// nil 與不含 token 的錯誤都要正常
	if b.scrubErr(nil) != "" {
		t.Error("nil error 應回空字串")
	}
	if got := b.scrubErr(errors.New("boom")); got != "boom" {
		t.Errorf("不含 token 的錯誤應原樣回傳，got %q", got)
	}

	// 空 token（理論上不會，但別把整個字串都遮成 ***）
	empty := &TelegramBot{token: ""}
	if got := empty.scrubErr(errors.New("boom")); got != "boom" {
		t.Errorf("空 token 不該影響訊息，got %q", got)
	}
}
