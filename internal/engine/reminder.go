package engine

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/yourname/go-tiny-claw/internal/schema"
)

// ReminderInjector 是死循环探测器：对 (工具名 + 参数) 做指纹，统计同一指纹的连续失败次数，
// 达到阈值就注入一条强力"跳出循环、换策略"的提醒，避免模型盯着同一个错误盲目重试烧 API。
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

// CheckAndInject 根据本轮最后一个工具的执行结果更新失败计数。
// 任意一次成功即清零；同一指纹连续失败 ≥3 次则返回一条强提醒消息（否则返回 nil）。
func (r *ReminderInjector) CheckAndInject(lastToolCall schema.ToolCall, lastResult schema.ToolResult) *schema.Message {
	fingerprint := generateFingerprint(lastToolCall.Name, lastToolCall.Arguments)

	if !lastResult.IsError {
		r.consecutiveFailures = make(map[string]int)
		return nil
	}

	r.consecutiveFailures[fingerprint]++
	failCount := r.consecutiveFailures[fingerprint]

	log.Printf("[Reminder] 监控到工具 %s 执行失败，该参数特征连续失败次数: %d\n", lastToolCall.Name, failCount)

	if failCount >= 3 {
		log.Println("[Reminder] ⚠️ 触发死循环干预！注入强力修正指令。")

		nudgeMsg := fmt.Sprintf(`[SYSTEM REMINDER 警告]
你似乎陷入了死循环。你刚刚连续 %d 次使用相同的参数调用了 '%s' 工具，并且都失败了。
请立即停止这种无效的重试！你的注意力被当前的报错过度吸引了。
你需要：
1. 停止猜测参数。跳出当前的局部思维。
2. 彻底改变你的策略。
3. 如果你确实无法通过系统工具解决当前问题，请直接结束任务并向用户说明你需要什么人工帮助，而不是继续盲目消耗 API 资源尝试。`, failCount, lastToolCall.Name)

		return &schema.Message{
			Role:    schema.RoleUser,
			Content: nudgeMsg,
		}
	}

	return nil
}
