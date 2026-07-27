package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// #5：面板稽核身分——有可信反代注入 X-Forwarded-User 就升級成具體人，否則退回預設；
// 標頭來自信任邊界，需截長 + 濾控制字元（免污染稽核記錄）。
func TestOperatorIDFrom(t *testing.T) {
	cases := []struct {
		name, header, want string
	}{
		{"無標頭→預設", "", operatorID},
		{"空白→預設", "   ", operatorID},
		{"具體人", "aaron@example.com", "aaron@example.com"},
		{"前後空白修掉", "  bob  ", "bob"},
		{"控制字元濾掉(防 log 注入)", "al\nice\tX", "aliceX"},
		{"全是控制字元→退回預設", "\n\t\r", operatorID},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/governance/authz-approve", nil)
		if c.header != "" {
			r.Header.Set("X-Forwarded-User", c.header)
		}
		if got := operatorIDFrom(r); got != c.want {
			t.Errorf("%s: operatorIDFrom=%q, 期望 %q", c.name, got, c.want)
		}
	}

	// 截長：>120 字元只留前 120
	long := strings.Repeat("a", 200)
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("X-Forwarded-User", long)
	if got := operatorIDFrom(r); len(got) != 120 {
		t.Errorf("超長標頭應截到 120，得到 %d", len(got))
	}
}
