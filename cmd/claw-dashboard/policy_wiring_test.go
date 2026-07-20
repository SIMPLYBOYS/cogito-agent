package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
	"github.com/SIMPLYBOYS/cogito-agent/internal/policy"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// 端對端釘住整條鏈：operator chat 的 registry 與 cron 共用，差別只在 ctx 的無人值守標記。
// 同一個守門中介層，必須因 ctx 不同而給出不同裁決。
func TestGuard_SameRegistryDifferentContext(t *testing.T) {
	guard := policy.Guard(&policy.Policy{}, chatbot.IsDangerousCommand,
		func(context.Context, schema.ToolCall) (bool, string) { return true, "" })

	executed := false
	next := func(context.Context, schema.ToolCall) schema.ToolResult {
		executed = true
		return schema.ToolResult{Output: "ran"}
	}
	// cat .env 命中內建的憑證路徑黑名單 → Ask
	call := schema.ToolCall{ID: "1", Name: "bash", Arguments: []byte(`{"command":"cat ../.env"}`)}

	// operator chat：人在鍵盤前 → 放行
	executed = false
	if res := guard(context.Background(), call, next); res.IsError {
		t.Errorf("operator 在現場時應放行：%s", res.Output)
	}
	if !executed {
		t.Error("operator 情境應實際執行")
	}

	// cron：同一個 guard、同一個 call，只差 ctx → 拒絕
	executed = false
	res := guard(policy.WithUnattended(context.Background()), call, next)
	if !res.IsError {
		t.Error("無人值守時讀取 .env 應被拒絕")
	}
	if executed {
		t.Error("被拒絕的操作不可執行")
	}
	if !strings.Contains(res.Output, "無人值守") {
		t.Errorf("應說明拒絕原因，實得：%s", res.Output)
	}
	var _ tools.MiddlewareFunc = guard // 型別確認：確實是可掛上 registry 的中介層
}

// 政策必須看得見：三種狀態都要在 platform 頁講清楚。
// 尤其「載入失敗」——那時 chat/cron 會拒絕啟動，使用者看到的現象是「chat 不能用」，
// 若面板不說原因，就只剩終端 log 裡那一行。
func TestPlatformPage_ShowsPolicyStatus(t *testing.T) {
	get := func(ws string) string {
		rec := httptest.NewRecorder()
		newServer(nil, "", ws, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/platform", nil))
		return rec.Body.String()
	}
	write := func(ws, content string) {
		t.Helper()
		dir := filepath.Join(ws, ".claw")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "policy.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 1. 未設定 → 說明走內建判斷
	if body := get(t.TempDir()); !strings.Contains(body, "走內建判斷") {
		t.Error("未設政策時應說明走內建判斷")
	}

	// 2. 有規則 → 列出工具、裁決、原因
	ws := t.TempDir()
	write(ws, `{"rules":[{"tool":"bash","match":"rm -rf","action":"deny","reason":"禁止遞迴刪除"}]}`)
	body := get(ws)
	for _, want := range []string{"bash", "rm -rf", "DENY", "禁止遞迴刪除", "1 條規則"} {
		if !strings.Contains(body, want) {
			t.Errorf("政策表格缺少 %q", want)
		}
	}

	// 3. 載入失敗 → 明講失敗與後果，不可靜默
	broken := t.TempDir()
	write(broken, `{"rules":[{"tool":"bash","action":"maybe"}]}`)
	body = get(broken)
	if !strings.Contains(body, "載入失敗") {
		t.Error("政策載入失敗應在頁面明示")
	}
	if !strings.Contains(body, "拒絕啟動") {
		t.Error("應說明後果（chat/排程會拒絕啟動），否則使用者不知道為何 chat 不能用")
	}
}
