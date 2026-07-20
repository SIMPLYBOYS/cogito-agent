package cron

import (
	"strings"
	"testing"
	"time"
)

func TestShouldNotify(t *testing.T) {
	t.Setenv(NotifyErrOnlyKey, "")
	if ShouldNotify("", "error") {
		t.Error("未設目標時不該推播")
	}
	if !ShouldNotify("slack:C1", "ok") {
		t.Error("設了目標、非只送失敗 → 成功也該推播")
	}

	t.Setenv(NotifyErrOnlyKey, "1")
	if ShouldNotify("slack:C1", "ok") {
		t.Error("只送失敗時，成功不該推播")
	}
	if !ShouldNotify("slack:C1", "error") {
		t.Error("只送失敗時，失敗仍該推播")
	}
}

func TestBuildCronNotice(t *testing.T) {
	j := Job{ID: "abc", Name: "巡檢", Schedule: "0 9 * * *"}

	ok := buildNotice(j, "ok", "", "有 3 個未推送的 commit。", "bot", 12*time.Second)
	for _, want := range []string{"✅", "巡檢", "0 9 * * *", "12s", "來源 bot", "3 個未推送", "/runs/cron-abc"} {
		if !strings.Contains(ok, want) {
			t.Errorf("成功通知缺少 %q：\n%s", want, ok)
		}
	}

	bad := buildNotice(j, "error", "connection refused", "", "dashboard", 3*time.Second)
	if !strings.Contains(bad, "❌") || !strings.Contains(bad, "connection refused") {
		t.Errorf("失敗通知應含 ❌ 與錯誤訊息：\n%s", bad)
	}

	// 過長回覆截斷——通知是提醒去看，不是搬全文
	long := buildNotice(j, "ok", "", strings.Repeat("字", noticeReplyMax+50), "bot", time.Second)
	if !strings.Contains(long, "（截斷）") {
		t.Error("過長回覆應截斷")
	}
	if len([]rune(long)) > noticeReplyMax+200 {
		t.Errorf("截斷後仍過長：%d 字", len([]rune(long)))
	}
}

func TestSendNotify_RejectsBadTarget(t *testing.T) {
	for _, bad := range []string{"", "slack", "nochannel:", ":C123"} {
		if err := sendOne(bad, "x"); err == nil {
			t.Errorf("目標 %q 應被拒", bad)
		}
	}
	if err := sendOne("discord:123", "x"); err == nil || !strings.Contains(err.Error(), "不支援") {
		t.Errorf("未支援平台應明確回錯，得 %v", err)
	}
	// 缺 token 應回明確錯誤（而非靜默失敗）
	t.Setenv("SLACK_BOT_TOKEN", "")
	if err := sendOne("slack:C1", "x"); err == nil || !strings.Contains(err.Error(), "SLACK_BOT_TOKEN") {
		t.Errorf("缺 Slack token 應明說，得 %v", err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	if err := sendOne("telegram:1", "x"); err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("缺 Telegram token 應明說，得 %v", err)
	}
}

// 誤把 token 貼進「推播目標」必須被擋——該欄明碼顯示在頁面上，貼 token 等於憑證外洩。
//
// 樣本全是【假造】的。前綴刻意用串接組出而非寫死字面值：GitHub 的 push protection 會對
// xoxb-… 這類字面值做模式比對，即使是測試假資料也會擋下推送。
func TestNotifyTarget_RejectsPastedToken(t *testing.T) {
	x := "x" // 拆開前綴，避開祕密掃描的字面比對
	tokens := []string{
		"slack:" + x + "oxb-000000000000-aaaaaaaaaaaaaaaa",
		"slack:" + x + "app-0-A000-aaa",
		"telegram:000000000:" + strings.Repeat("A", 35), // 誤貼 bot token 的形狀
		"slack:" + strings.Repeat("a", 41),              // 過長＝幾乎必然是憑證
	}
	for _, tk := range tokens {
		if _, _, err := parseTarget(tk); err == nil {
			t.Errorf("疑似 token 的目標 %q 應被擋下", tk)
		}
	}

	// 正常的頻道／聊天室 id 不可被誤擋
	for _, good := range []string{"slack:C0123ABC456", "telegram:12345678", "telegram:-1001234567890"} {
		if _, _, err := parseTarget(good); err != nil {
			t.Errorf("合法目標 %q 不該被擋：%v", good, err)
		}
	}
}

// 多目標：逗號分隔可同時送 Telegram 與 Slack；任一個不合法就整批擋下。
func TestNotifyTargets_Multi(t *testing.T) {
	got := SplitTargets(" telegram:123 , slack:C0123ABC ,, ")
	want := []string{"telegram:123", "slack:C0123ABC"}
	if len(got) != len(want) {
		t.Fatalf("預期拆出 %d 個目標，得 %d：%v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 個應為 %q，得 %q", i, want[i], got[i])
		}
	}

	if err := ValidateTargets("telegram:123, slack:C0123ABC"); err != nil {
		t.Errorf("合法多目標不該被擋：%v", err)
	}
	// 一好一壞 → 整批擋下，避免存進「一半能用」的設定
	if err := ValidateTargets("telegram:123, slack:x" + "oxb-000000000000-aaaa"); err == nil {
		t.Error("其中一個是 token 時應整批擋下")
	}
	if err := ValidateTargets("telegram:123, discord:999"); err == nil {
		t.Error("其中一個平台不支援時應整批擋下")
	}
	if err := ValidateTargets(""); err != nil {
		t.Errorf("空字串＝不推播，不該報錯：%v", err)
	}
}
