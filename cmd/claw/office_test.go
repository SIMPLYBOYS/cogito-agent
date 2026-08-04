package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// 綁定政策：這條是「能執行任意任務的入口別意外對外曝光」的唯一防線。
func TestOfficeBindDenied(t *testing.T) {
	cases := []struct {
		addr     string
		insecure bool
		want     bool // true＝應拒開
	}{
		{"127.0.0.1:8123", false, false}, // loopback：放行
		{"localhost:8123", false, false},
		{"[::1]:8123", false, false},
		{"0.0.0.0:8123", false, true}, // 對外：拒
		{":8123", false, true},        // 所有介面：拒
		{"192.168.1.5:8123", false, true},
		{"0.0.0.0:8123", true, false}, // 顯式 insecure：放行（自負風險）
	}
	for _, c := range cases {
		if got := officeBindDenied(c.addr, c.insecure); got != c.want {
			t.Errorf("officeBindDenied(%q, insecure=%v)=%v, 期望 %v", c.addr, c.insecure, got, c.want)
		}
	}
}

func TestOfficeTaskHandler(t *testing.T) {
	var gotChannel, gotUser, gotText string
	var dispatched int
	h := officeTaskHandler("s3cret", "office-web", func(channelID, userID, text string) {
		dispatched++
		gotChannel, gotUser, gotText = channelID, userID, text
	})

	do := func(method, auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/task", strings.NewReader(body))
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	// 未帶 token / 錯 token → 401，且【絕不】派工
	for _, auth := range []string{"", "Bearer wrong", "Bearer ", "s3cret"} {
		if code := do(http.MethodPost, auth, `{"agent":"p01","text":"x"}`).Code; code != http.StatusUnauthorized {
			t.Errorf("auth=%q 應回 401，得到 %d", auth, code)
		}
	}
	if dispatched != 0 {
		t.Fatalf("未通過 auth 不該派工，卻派了 %d 次", dispatched)
	}

	// 非 POST → 405（且不派工）
	if code := do(http.MethodGet, "Bearer s3cret", "").Code; code != http.StatusMethodNotAllowed {
		t.Errorf("GET 應回 405，得到 %d", code)
	}

	// 缺欄位 → 400
	for _, body := range []string{`{}`, `{"agent":"p01"}`, `{"text":"x"}`, `not-json`} {
		if code := do(http.MethodPost, "Bearer s3cret", body).Code; code != http.StatusBadRequest {
			t.Errorf("body=%q 應回 400，得到 %d", body, code)
		}
	}

	// 超過 1 MB 的 body → 400（MaxBytesReader 擋下，不是吃掉記憶體）
	huge := `{"agent":"p01","text":"` + strings.Repeat("a", 2<<20) + `"}`
	if code := do(http.MethodPost, "Bearer s3cret", huge).Code; code != http.StatusBadRequest {
		t.Errorf("超大 body 應回 400，得到 %d", code)
	}
	if dispatched != 0 {
		t.Fatalf("以上皆不該派工，卻派了 %d 次", dispatched)
	}

	// 正確 token + 合法 body → 202，派工參數正確（user 由伺服器端決定，不從 body 取）
	w := do(http.MethodPost, "Bearer s3cret", `{"agent":"p01","text":"跑個測試"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("合法請求應回 202，得到 %d", w.Code)
	}
	if dispatched != 1 || gotChannel != "p01" || gotUser != "office-web" || gotText != "跑個測試" {
		t.Errorf("派工參數錯誤：n=%d channel=%q user=%q text=%q", dispatched, gotChannel, gotUser, gotText)
	}
}

// 能力清單同樣要 token：它會透露這台機器掛了哪些 MCP 工具與內部技能名稱。
func TestOfficeCapsHandler(t *testing.T) {
	called := ""
	h := officeCapsHandler("s3cret", func(agent string) ([]schema.ToolDefinition, []ctxpkg.Skill) {
		called = agent
		return []schema.ToolDefinition{{Name: "bash", Description: "跑指令"}},
			[]ctxpkg.Skill{{Name: "git-workflow", Description: "提交流程"}}
	})

	for _, tc := range []struct {
		name, auth, query string
		want              int
	}{
		{"沒帶 token", "", "?agent=p01", http.StatusUnauthorized},
		{"錯的 token", "Bearer nope", "?agent=p01", http.StatusUnauthorized},
		{"少了 agent", "Bearer s3cret", "", http.StatusBadRequest},
		{"正常", "Bearer s3cret", "?agent=p01", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, "/capabilities"+tc.query, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != tc.want {
			t.Errorf("%s: 狀態碼 %d，期望 %d", tc.name, w.Code, tc.want)
		}
	}
	if called != "p01" {
		t.Errorf("agent 沒有傳到 caps()：%q", called)
	}
	req := httptest.NewRequest(http.MethodGet, "/capabilities?agent=p01", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	w := httptest.NewRecorder()
	h(w, req)
	body := w.Body.String()
	for _, want := range []string{`"tools"`, `"bash"`, `"skills"`, `"git-workflow"`} {
		if !strings.Contains(body, want) {
			t.Errorf("回應缺少 %s：%s", want, body)
		}
	}
}
