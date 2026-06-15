package engine

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
)

// ReminderInjector 是死循環探測器：對 (工具名 + 參數) 做指紋，統計同一指紋的連續失敗次數，
// 達到閾值就注入一條強力"跳出循環、換策略"的提醒，避免模型盯著同一個錯誤盲目重試燒 API。
type ReminderInjector struct {
	consecutiveFailures map[string]int
}

func NewReminderInjector() *ReminderInjector {
	return &ReminderInjector{
		consecutiveFailures: make(map[string]int),
	}
}

func generateFingerprint(toolName string, args []byte) string {
	hasher := md5.New()
	hasher.Write([]byte(toolName))
	hasher.Write(args)
	return hex.EncodeToString(hasher.Sum(nil))
}

// CheckAndInject 根據本輪最後一個工具的執行結果更新失敗計數。
// 任意一次成功即清零；同一指紋連續失敗 ≥3 次則返回一條強提醒消息（否則返回 nil）。
func (r *ReminderInjector) CheckAndInject(lastToolCall schema.ToolCall, lastResult schema.ToolResult) *schema.Message {
	fingerprint := generateFingerprint(lastToolCall.Name, lastToolCall.Arguments)

	if !lastResult.IsError {
		r.consecutiveFailures = make(map[string]int)
		return nil
	}

	r.consecutiveFailures[fingerprint]++
	failCount := r.consecutiveFailures[fingerprint]

	log.Printf("[Reminder] 監控到工具 %s 執行失敗，該參數特徵連續失敗次數: %d\n", lastToolCall.Name, failCount)

	if failCount >= 3 {
		log.Println("[Reminder] ⚠️ 觸發死循環干預！注入強力修正指令。")

		nudgeMsg := fmt.Sprintf(`[SYSTEM REMINDER 警告]
你似乎陷入了死循環。你剛剛連續 %d 次使用相同的參數調用了 '%s' 工具，並且都失敗了。
請立即停止這種無效的重試！你的注意力被當前的報錯過度吸引了。
你需要：
1. 停止猜測參數。跳出當前的局部思維。
2. 徹底改變你的策略。
3. 如果你確實無法通過系統工具解決當前問題，請直接結束任務並向用戶說明你需要什麼人工幫助，而不是繼續盲目消耗 API 資源嘗試。`, failCount, lastToolCall.Name)

		return &schema.Message{
			Role:    schema.RoleUser,
			Content: nudgeMsg,
		}
	}

	return nil
}
