package slackbot

import (
	"strings"
	"testing"
	"time"
)

func TestIsDangerousCommand(t *testing.T) {
	cases := []struct {
		tool string
		args string
		want bool
	}{
		{"bash", `{"command":"rm -rf /tmp/x"}`, true},
		{"bash", `{"command":"sudo apt install foo"}`, true},
		{"bash", `{"command":"drop table users"}`, true},
		{"bash", `{"command":"echo '' > main.go"}`, true}, // >.*\.go 覆盖源码
		{"bash", `{"command":"ls -la"}`, false},
		{"bash", `{"command":"go build ./..."}`, false},
		{"bash", `{"command":"nginx -s reload"}`, true},        // ch22: 重启服务
		{"bash", `{"command":"systemctl restart nginx"}`, true}, // ch22: 系统服务
		{"bash", `{"command":"kill -9 1234"}`, true},            // ch22: 杀进程
		{"read_file", `{"path":"/etc/passwd"}`, false},          // 只读工具永远放行
		{"write_file", `{"path":"main.go"}`, false},             // ch16 已知局限：白名单内但暂无检查
		{"edit_file", `{"path":"main.go"}`, false},
	}
	for _, c := range cases {
		if got := IsDangerousCommand(c.tool, c.args); got != c.want {
			t.Errorf("IsDangerousCommand(%q, %q) = %v, 期望 %v", c.tool, c.args, got, c.want)
		}
	}
}

func newTestMgr(timeout time.Duration) *ApprovalManager {
	return &ApprovalManager{pendingTasks: make(map[string]*pendingTask), Timeout: timeout}
}

// 按 taskID 精确唤醒（兼容形式 approve <id>）。
func TestApprovalManager_RoundTrip(t *testing.T) {
	m := newTestMgr(time.Minute)

	ready := make(chan struct{})
	done := make(chan struct{})
	var allowed bool
	var reason string

	go func() {
		allowed, reason = m.WaitForApproval("task1", "chA", "bash", "rm -rf /", func(string) { close(ready) })
		close(done)
	}()

	<-ready
	if !m.ResolveApproval("task1", true, "ok") {
		t.Fatal("ResolveApproval 应命中等待中的任务")
	}
	<-done

	if !allowed || reason != "ok" {
		t.Errorf("审批结果不对: allowed=%v reason=%q", allowed, reason)
	}
	if m.ResolveApproval("does-not-exist", true, "") {
		t.Error("不存在的 taskID 不应被命中")
	}
}

// 弱点修补①：裸 approve 按频道解析，且不误伤其他频道。
func TestApprovalManager_ResolveByChannel(t *testing.T) {
	m := newTestMgr(time.Minute)

	ready := make(chan struct{}, 1)
	done := make(chan struct{})
	var allowed bool

	go func() {
		allowed, _ = m.WaitForApproval("t1", "chX", "bash", "kill 1", func(string) { ready <- struct{}{} })
		close(done)
	}()

	<-ready
	if n := m.ResolveByChannel("chX", true, "ok"); n != 1 {
		t.Fatalf("应解决本频道 1 个待审批，got %d", n)
	}
	<-done
	if !allowed {
		t.Error("应被批准")
	}
	if n := m.ResolveByChannel("chOther", true, ""); n != 0 {
		t.Errorf("其他频道无 pending，应返回 0，got %d", n)
	}
}

// 弱点修补③：无人响应时超时自动拒绝并清理，不泄漏 goroutine。
func TestApprovalManager_Timeout(t *testing.T) {
	m := newTestMgr(50 * time.Millisecond)

	start := time.Now()
	allowed, reason := m.WaitForApproval("t1", "chA", "bash", "x", func(string) {})
	elapsed := time.Since(start)

	if allowed {
		t.Error("超时应自动拒绝")
	}
	if !strings.Contains(reason, "超时") {
		t.Errorf("拒绝原因应含'超时': %q", reason)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("应大致等待 timeout 时长，实际 %v", elapsed)
	}
	if m.ResolveApproval("t1", true, "") {
		t.Error("超时后该 task 应已从 pending 清理")
	}
}
