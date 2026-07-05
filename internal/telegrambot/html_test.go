package telegrambot

import (
	"strings"
	"testing"
)

func TestTelegramHTML(t *testing.T) {
	// 表格 ``` block → <pre>；**粗體** → <b>；&<> 被 escape
	out := telegramHTML("**標題**\n```\n職稱  薪資\nAI    400萬\n```\n a < b & c")
	if !strings.Contains(out, "<b>標題</b>") {
		t.Errorf("粗體應轉 <b>，got %q", out)
	}
	if !strings.Contains(out, "<pre>") || !strings.Contains(out, "</pre>") {
		t.Errorf("``` 應轉 <pre>，got %q", out)
	}
	if !strings.Contains(out, "a &lt; b &amp; c") {
		t.Errorf("&<> 應被 escape，got %q", out)
	}
	if strings.Contains(out, "```") {
		t.Error("不應殘留 ``` 圍欄")
	}
}
