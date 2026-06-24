package slackbot

import (
	"strings"
	"testing"
)

func TestBotMentionRegexp_StripsBothForms(t *testing.T) {
	re := botMentionRegexp("U123")
	cases := map[string]string{
		"<@U123> approve":             "approve",      // 純 ID
		"<@U123|cogito> apply memory": "apply memory", // 帶顯示名
		"<@U123|bot> reject memory":   "reject memory",
		"<@U123> 幫我寫 fizzbuzz":        "幫我寫 fizzbuzz",
	}
	for in, want := range cases {
		got := strings.TrimSpace(re.ReplaceAllString(in, ""))
		if got != want {
			t.Errorf("strip(%q)=%q，want %q", in, got, want)
		}
	}
}
