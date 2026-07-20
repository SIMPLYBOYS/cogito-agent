package main

import (
	"context"
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
