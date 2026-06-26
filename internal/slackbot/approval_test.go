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
		{"bash", `{"command":"echo '' > main.go"}`, true}, // >.*\.go 覆蓋源碼
		{"bash", `{"command":"ls -la"}`, false},
		{"bash", `{"command":"go build ./..."}`, false},
		{"bash", `{"command":"nginx -s reload"}`, true},           // 重啟服務
		{"bash", `{"command":"systemctl restart nginx"}`, true},   // 系統服務
		{"bash", `{"command":"kill -9 1234"}`, true},              // 殺進程
		{"read_file", `{"path":"/etc/passwd"}`, false},            // 只讀工具永遠放行
		{"write_file", `{"path":"main.go"}`, false},               // 工作區內正常源碼寫入，放行
		{"write_file", `{"path":"src/app/handler.go"}`, false},    // 子目錄正常寫入，放行
		{"edit_file", `{"path":"main.go"}`, false},                // 正常編輯，放行
		{"write_file", `{"path":"/etc/passwd"}`, true},            // 絕對路徑逃出工作區
		{"write_file", `{"path":"../../etc/cron"}`, true},         // .. 穿越逃出工作區
		{"write_file", `{"path":".env"}`, true},                   // 機密文件
		{"edit_file", `{"path":"config/.env.production"}`, true},  // 機密文件 .env.*
		{"write_file", `{"path":".git/hooks/pre-commit"}`, true},  // 版控目錄
		{"edit_file", `{"path":".claw/skills/x/SKILL.md"}`, true}, // 自身配置/技能目錄

		// 遠端 MCP 工具（經 gateway 的 mcp_call_tool）
		{"mcp_call_tool", `{"name":"filesystem__delete_file","arguments":{"path":"/x"}}`, true},   // 破壞性動詞
		{"mcp_call_tool", `{"name":"fs__write_file","arguments":{"path":"a"}}`, true},             // write
		{"mcp_call_tool", `{"name":"shell__run","arguments":{"cmd":"rm -rf /tmp"}}`, true},        // 參數命中 bash 危險模式
		{"mcp_call_tool", `{"name":"server__get","arguments":{"path":"~/.ssh/id_rsa"}}`, true},    // 憑證路徑
		{"mcp_call_tool", `{"name":"twinkle-hub__opendata_query","arguments":{"q":"房價"}}`, false}, // 讀類查詢，放行
		{"mcp_call_tool", `{"name":"twtools__address_lookup","arguments":{"addr":"台北"}}`, false},  // 讀類，放行
		{"mcp_describe_tool", `{"name":"filesystem__delete_file"}`, false},                        // 只查 schema，永遠放行
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

// 按 taskID 精確喚醒（兼容形式 approve <id>）。
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
		t.Fatal("ResolveApproval 應命中等待中的任務")
	}
	<-done

	if !allowed || reason != "ok" {
		t.Errorf("審批結果不對: allowed=%v reason=%q", allowed, reason)
	}
	if m.ResolveApproval("does-not-exist", true, "") {
		t.Error("不存在的 taskID 不應被命中")
	}
}

// 弱點修補①：裸 approve 按頻道解析，且不誤傷其他頻道。
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
		t.Fatalf("應解決本頻道 1 個待審批，got %d", n)
	}
	<-done
	if !allowed {
		t.Error("應被批准")
	}
	if n := m.ResolveByChannel("chOther", true, ""); n != 0 {
		t.Errorf("其他頻道無 pending，應返回 0，got %d", n)
	}
}

// 弱點修補③：無人響應時超時自動拒絕並清理，不洩漏 goroutine。
func TestApprovalManager_Timeout(t *testing.T) {
	m := newTestMgr(50 * time.Millisecond)

	start := time.Now()
	allowed, reason := m.WaitForApproval("t1", "chA", "bash", "x", func(string) {})
	elapsed := time.Since(start)

	if allowed {
		t.Error("超時應自動拒絕")
	}
	if !strings.Contains(reason, "超時") {
		t.Errorf("拒絕原因應含'超時': %q", reason)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("應大致等待 timeout 時長，實際 %v", elapsed)
	}
	if m.ResolveApproval("t1", true, "") {
		t.Error("超時後該 task 應已從 pending 清理")
	}
}
