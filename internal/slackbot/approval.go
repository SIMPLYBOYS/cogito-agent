package slackbot

import (
	"fmt"
	"log"
	"regexp"
	"sync"
)

// ApprovalManager 是 channel-based 的全局审批单例：危险工具调用在 WaitForApproval 处阻塞，
// 直到人类通过 Slack 回复 approve/reject，由 ResolveApproval 喂入结果唤醒。
// 呼应 GlobalSessionMgr 的包级单例风格。
type ApprovalResult struct {
	Allowed bool
	Reason  string
}

type ApprovalManager struct {
	mu           sync.Mutex
	pendingTasks map[string]chan ApprovalResult
}

var GlobalApprovalMgr = &ApprovalManager{
	pendingTasks: make(map[string]chan ApprovalResult),
}

// WaitForApproval 注册审批请求、通过 notify 把请求推给人类，然后阻塞当前 goroutine
// 直到 ResolveApproval 喂入结果。notify 解耦了「怎么通知」（如发到对应 Slack 频道）。
func (m *ApprovalManager) WaitForApproval(taskID, toolName, args string, notify func(text string)) (bool, string) {
	ch := make(chan ApprovalResult, 1) // 容量 1，避免 sender 阻塞

	m.mu.Lock()
	m.pendingTasks[taskID] = ch
	m.mu.Unlock()

	notice := fmt.Sprintf("⚠️ *高危操作审批请求*\nAgent 试图执行：\n• 工具: `%s`\n• 参数: `%s`\n任务 ID: `%s`\n👉 请回复 `approve %s` 或 `reject %s` 决定是否放行。",
		toolName, args, taskID, taskID, taskID)
	if notify != nil {
		notify(notice)
	} else {
		fmt.Printf("\n[需要审批 TaskID: %s]\n%s\n", taskID, notice)
	}

	log.Printf("[Approval] 发送审批请求 (TaskID: %s)，协程挂起等待...\n", taskID)
	result := <-ch // ★ 阻塞点：等管理员回复 ★

	m.mu.Lock()
	delete(m.pendingTasks, taskID)
	m.mu.Unlock()

	return result.Allowed, result.Reason
}

// ResolveApproval 在收到 approve/reject 消息时调用，唤醒等待中的 goroutine。
// 返回是否命中一个等待中的任务（用于忽略无效/过期的口令）。
func (m *ApprovalManager) ResolveApproval(taskID string, allowed bool, reason string) bool {
	m.mu.Lock()
	ch, exists := m.pendingTasks[taskID]
	m.mu.Unlock()

	if exists {
		log.Printf("[Approval] 收到审批结果 (TaskID: %s, Allowed: %v)\n", taskID, allowed)
		ch <- ApprovalResult{Allowed: allowed, Reason: reason}
		return true
	}
	return false
}

// IsDangerousCommand 判断工具调用是否命中高危黑名单。
// 注意（ch16 已知局限）：write_file / edit_file 在白名单内但暂无检查逻辑，是预留扩展点。
func IsDangerousCommand(toolName string, args string) bool {
	if toolName != "bash" && toolName != "write_file" && toolName != "edit_file" {
		return false
	}
	if toolName == "bash" {
		dangerousPatterns := []string{
			`rm\s+-r`,      // 递归删除
			`sudo\s+`,      // 提权
			`drop\s+`,      // SQL DROP
			`>.*\.go`,      // 重定向覆盖 .go 文件（防 LLM 绝望时清空源码）
			`nginx\s+-s`,   // ch22: 重启/停止 nginx（会中断线上服务）
			`systemctl\s+`, // ch22: 系统服务管理（start/stop/restart）
			`kill\s+`,      // ch22: 杀进程
		}
		for _, p := range dangerousPatterns {
			if matched, _ := regexp.MatchString(p, args); matched {
				return true
			}
		}
	}
	return false
}
