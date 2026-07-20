package policy

import (
	"context"
	"fmt"
	"log"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

type unattendedKey struct{}

// WithUnattended 標記「這條執行鏈沒有人在現場」（排程任務用）。
//
// 【為何走 context 而非各建一套 registry】operator chat 與 cron 共用同一個組好的 agent／registry，
// 中介層是掛在 registry 上的，無法按「這次是誰觸發」給不同規則。ctx 才是隨每次執行流動的東西。
func WithUnattended(ctx context.Context) context.Context {
	return context.WithValue(ctx, unattendedKey{}, true)
}

// IsUnattended 回這條執行鏈是否無人值守。
func IsUnattended(ctx context.Context) bool {
	v, _ := ctx.Value(unattendedKey{}).(bool)
	return v
}

// Asker 詢問人類是否放行。回 (false, 原因) 即拒絕。
type Asker func(ctx context.Context, call schema.ToolCall) (allowed bool, reason string)

// Guard 組出工具呼叫的守門中介層，裁決順序 Deny > Ask > Allow。
//
//   - 政策檔有裁決 → 依政策。
//   - 政策沒意見 → 落回 builtinDangerous（既有的危險黑名單）：命中＝Ask，否則 Allow。
//   - Ask 的處理分兩種情境：
//     有人在現場（IM 有審批流程、CLI/面板有人盯著）→ 交給 asker；
//     【無人值守】（cron）→ 一律當 Deny。沒有人可以問的時候，「等人回答」不是安全，是碰運氣。
//
// asker 為 nil 代表這個入口沒有審批管道，等同無人值守。
func Guard(p *Policy, builtinDangerous func(tool, args string) bool, asker Asker) tools.MiddlewareFunc {
	return func(ctx context.Context, call schema.ToolCall, next tools.ToolHandler) schema.ToolResult {
		args := string(call.Arguments)

		action, reason := p.Decide(call.Name, args)
		if action == "" { // 政策沒意見 → 內建黑名單
			if builtinDangerous != nil && builtinDangerous(call.Name, args) {
				action, reason = Ask, "命中高危黑名單"
			} else {
				action = Allow
			}
		}

		switch action {
		case Deny:
			log.Printf("[policy] 拒絕工具 %s：%s", call.Name, reason)
			return deniedResult(call, fmt.Sprintf("政策拒絕執行。原因: %s", orDefault(reason, "policy deny")))

		case Ask:
			if asker == nil || IsUnattended(ctx) {
				// 無人可問 → fail-closed。排程任務不該因為「沒人回答」而卡到逾時。
				log.Printf("[policy] 無人值守，拒絕需審批的工具 %s：%s", call.Name, reason)
				return deniedResult(call, fmt.Sprintf(
					"此操作需人工審批，但目前為【無人值守】執行（如排程任務），已自動拒絕。原因: %s",
					orDefault(reason, "高危操作")))
			}
			if allowed, why := asker(ctx, call); !allowed {
				return deniedResult(call, fmt.Sprintf("執行被系統攔截。原因: %s", why))
			}
		}
		return next(ctx, call)
	}
}

func deniedResult(call schema.ToolCall, msg string) schema.ToolResult {
	return schema.ToolResult{ToolCallID: call.ID, Output: msg, IsError: true}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
