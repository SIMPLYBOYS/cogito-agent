package slackbot

import (
	"fmt"
	"log"
	"regexp"
	"sync"
	"time"
)

// ApprovalManager 是 channel-based 的全局审批单例：危险工具调用在 WaitForApproval 处阻塞，
// 直到人类通过 Slack 回复 approve/reject，由 ResolveApproval / ResolveByChannel 喂入结果唤醒；
// 超过 Timeout 无人响应则自动拒绝，避免 goroutine 永久泄漏。
type ApprovalResult struct {
	Allowed bool
	Reason  string
}

// pendingTask 记录一个等待中的审批：结果 channel + 发起它的频道（用于裸 approve 的按频道解析）。
type pendingTask struct {
	ch        chan ApprovalResult
	channelID string
}

const defaultApprovalTimeout = 5 * time.Minute

type ApprovalManager struct {
	mu           sync.Mutex
	pendingTasks map[string]*pendingTask
	Timeout      time.Duration // <=0 时回退到 defaultApprovalTimeout
}

var GlobalApprovalMgr = &ApprovalManager{
	pendingTasks: make(map[string]*pendingTask),
	Timeout:      defaultApprovalTimeout,
}

// WaitForApproval 注册审批请求、通过 notify 推送给人类，然后阻塞当前 goroutine 等待结果。
// 弱点修补③：超过 Timeout 无响应自动拒绝并清理，杜绝 goroutine 永久挂起。
func (m *ApprovalManager) WaitForApproval(taskID, channelID, toolName, args string, notify func(text string)) (bool, string) {
	ch := make(chan ApprovalResult, 1) // 容量 1，避免 sender 阻塞

	m.mu.Lock()
	m.pendingTasks[taskID] = &pendingTask{ch: ch, channelID: channelID}
	m.mu.Unlock()

	timeout := m.Timeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}

	notice := fmt.Sprintf("⚠️ *高危操作审批请求*\nAgent 试图执行：\n• 工具: `%s`\n• 参数: `%s`\n任务 ID: `%s`\n👉 直接回复 `approve` / `reject` 即可（也可带 ID：`approve %s`）。%.0f 分钟内无响应将自动拒绝。",
		toolName, args, taskID, taskID, timeout.Minutes())
	if notify != nil {
		notify(notice)
	} else {
		fmt.Printf("\n[需要审批 TaskID: %s]\n%s\n", taskID, notice)
	}

	log.Printf("[Approval] 发送审批请求 (TaskID: %s, 频道: %s)，协程挂起等待...\n", taskID, channelID)

	select {
	case result := <-ch:
		m.remove(taskID)
		return result.Allowed, result.Reason
	case <-time.After(timeout):
		m.remove(taskID)
		log.Printf("[Approval] ⏰ 审批超时 (TaskID: %s)，自动拒绝。\n", taskID)
		return false, fmt.Sprintf("审批超时（%v 无人响应），已自动拒绝该操作。", timeout)
	}
}

func (m *ApprovalManager) remove(taskID string) {
	m.mu.Lock()
	delete(m.pendingTasks, taskID)
	m.mu.Unlock()
}

// ResolveApproval 按 taskID 精确唤醒一个等待中的审批。返回是否命中。
func (m *ApprovalManager) ResolveApproval(taskID string, allowed bool, reason string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	pt, ok := m.pendingTasks[taskID]
	if !ok {
		return false
	}
	delete(m.pendingTasks, taskID)
	pt.ch <- ApprovalResult{Allowed: allowed, Reason: reason} // ch 有缓冲，持锁发送不阻塞
	return true
}

// ResolveByChannel 唤醒某频道下所有等待中的审批（弱点修补①：裸 approve/reject 无需手打长 taskID）。
// 返回处理的数量。
func (m *ApprovalManager) ResolveByChannel(channelID string, allowed bool, reason string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for id, pt := range m.pendingTasks {
		if pt.channelID == channelID {
			delete(m.pendingTasks, id)
			pt.ch <- ApprovalResult{Allowed: allowed, Reason: reason}
			count++
		}
	}
	return count
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
