package slackbot

import "testing"

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
		{"bash", `{"command":"nginx -s reload"}`, true},       // ch22: 重启服务
		{"bash", `{"command":"systemctl restart nginx"}`, true}, // ch22: 系统服务
		{"bash", `{"command":"kill -9 1234"}`, true},            // ch22: 杀进程
		{"read_file", `{"path":"/etc/passwd"}`, false}, // 只读工具永远放行
		{"write_file", `{"path":"main.go"}`, false},    // ch16 已知局限：白名单内但暂无检查
		{"edit_file", `{"path":"main.go"}`, false},
	}
	for _, c := range cases {
		if got := IsDangerousCommand(c.tool, c.args); got != c.want {
			t.Errorf("IsDangerousCommand(%q, %q) = %v, 期望 %v", c.tool, c.args, got, c.want)
		}
	}
}

// 验证 channel 同步：WaitForApproval 阻塞，ResolveApproval 唤醒并传回结果。
func TestApprovalManager_RoundTrip(t *testing.T) {
	m := &ApprovalManager{pendingTasks: make(map[string]chan ApprovalResult)}

	ready := make(chan struct{})
	done := make(chan struct{})
	var allowed bool
	var reason string

	go func() {
		// notify 在 pendingTasks 注册之后才触发，用它作为"已就绪"信号，避免轮询
		allowed, reason = m.WaitForApproval("task1", "bash", "rm -rf /", func(string) { close(ready) })
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

	// 不存在的 taskID 不应命中
	if m.ResolveApproval("does-not-exist", true, "") {
		t.Error("不存在的 taskID 不应被 ResolveApproval 命中")
	}
}
