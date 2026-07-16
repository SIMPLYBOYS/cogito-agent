package chatbot

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isRecoverableErr 是自動續跑的閘：把終局錯誤誤判成可續跑 → 無限重試燒錢，故釘死分類。
func TestIsRecoverableErr(t *testing.T) {
	recoverable := []error{
		&net.OpError{Op: "dial", Err: errors.New("connection refused")},
		fmt.Errorf("Action 階段失敗: %w", errors.New("dial tcp: i/o timeout")),
		errors.New("anthropic: 529 overloaded_error"),
		errors.New("rate limit exceeded (429)"),
		errors.New("unexpected EOF"),
	}
	for _, e := range recoverable {
		if !isRecoverableErr(e) {
			t.Errorf("應判為可續跑: %v", e)
		}
	}
	terminal := []error{
		nil,
		errors.New("達到最大回合數上限 40，強制終止"),
		errors.New("達到單任務成本上限 $1.00（本次已花費 $1.20），強制終止"),
		errors.New("工具參數解析失敗"),
	}
	for _, e := range terminal {
		if isRecoverableErr(e) {
			t.Errorf("應判為終局（不續跑）: %v", e)
		}
	}
}

// per-channel Plan Mode 切換：指令改的是該頻道 Session 的狀態（factory 據此建引擎）。
func TestTryPlanCommand_TogglesSession(t *testing.T) {
	c := NewCore("planttest", t.TempDir(), nil, func(string, string) {})
	conv := "planttest:chanA"

	if !c.tryPlanCommand(conv, "plan on") {
		t.Fatal("`plan on` 應被消費")
	}
	if !c.sessionFor(conv).PlanMode() {
		t.Error("`plan on` 後該頻道應為開")
	}
	if !c.tryPlanCommand(conv, "PLAN OFF") { // 大小寫不敏感
		t.Fatal("`plan off` 應被消費")
	}
	if c.sessionFor(conv).PlanMode() {
		t.Error("`plan off` 後該頻道應為關")
	}
	if c.tryPlanCommand(conv, "幫我寫個 fizzbuzz") {
		t.Error("一般任務文字不該被當成 plan 指令消費")
	}
}

func TestResumeBackoff_Exponential(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: 2 * time.Second, 2: 4 * time.Second, 3: 8 * time.Second} {
		if got := resumeBackoff(attempt); got != want {
			t.Errorf("resumeBackoff(%d)=%v, want %v", attempt, got, want)
		}
	}
}

// per-channel 隔離：不同頻道得到不同目錄、同頻道穩定、都在 root 之下。
func TestChannelWorkDir_PerChannelIsolation(t *testing.T) {
	b := &Core{workDir: "/srv/ws"}

	a := b.channelWorkDir("C123")
	c := b.channelWorkDir("C999")
	if a == c {
		t.Fatalf("不同頻道應得到不同目錄: %s == %s", a, c)
	}
	if a != b.channelWorkDir("C123") {
		t.Fatal("同頻道應穩定得到同一目錄")
	}
	root := filepath.Clean("/srv/ws")
	if !strings.HasPrefix(filepath.Clean(a), root+string(filepath.Separator)) {
		t.Errorf("頻道目錄應在 root 之下: %s", a)
	}
}

// 安全：含路徑穿越的 channelID 必須被清理，不能逃出 root。
func TestChannelWorkDir_SanitizesTraversal(t *testing.T) {
	b := &Core{workDir: "/srv/ws"}
	got := b.channelWorkDir("../../etc")
	if strings.Contains(got, "..") {
		t.Errorf("不應含 .. 路徑穿越: %s", got)
	}
	root := filepath.Clean("/srv/ws")
	if !strings.HasPrefix(filepath.Clean(got), root+string(filepath.Separator)) {
		t.Errorf("清理後仍應被限制在 root 之下: %s", got)
	}
}

func TestSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		"C0123ABCD": "C0123ABCD",
		"../../etc": "______etc",
		"a/b":       "a_b",
		"":          "default",
	}
	for in, want := range cases {
		if got := sanitizeSegment(in); got != want {
			t.Errorf("sanitizeSegment(%q)=%q, 期望 %q", in, got, want)
		}
	}
}

// per-WorkDir 鎖：同一 key 互斥（序列化），不同 key 可並行；釋放後可再取得。
func TestWorkspaceLock_PerKeyMutualExclusion(t *testing.T) {
	b := &Core{}
	noop := func() {}
	wdA := "/srv/ws/channels/A"
	wdB := "/srv/ws/channels/B"
	// running 是 package 級的（跨 Core 共用），測試結束要還原，否則 -count=2 重跑會撞到殘留。
	t.Cleanup(func() { b.release(wdA); b.release(wdB) })

	if !b.tryAcquire(wdA, noop) {
		t.Fatal("首次應取得 A")
	}
	if b.tryAcquire(wdA, noop) {
		t.Fatal("A 忙碌中，同目錄第二次應失敗（序列化）")
	}
	if !b.tryAcquire(wdB, noop) {
		t.Fatal("不同目錄 B 應可並行取得")
	}

	b.release(wdA)
	if !b.tryAcquire(wdA, noop) {
		t.Fatal("釋放後 A 應可再取得")
	}
}

// /stop：stop 取消登記的 cancel 並回 true；沒有執行中則回 false。
func TestStop(t *testing.T) {
	c := &Core{}
	wd := "/srv/ws/channels/X"
	t.Cleanup(func() { c.release(wd) }) // running 是 package 級的，見上

	if c.stop(wd) {
		t.Fatal("沒有執行中任務時 stop 應回 false")
	}
	cancelled := false
	c.tryAcquire(wd, func() { cancelled = true })
	if !c.stop(wd) {
		t.Fatal("有執行中任務時 stop 應回 true")
	}
	if !cancelled {
		t.Fatal("stop 應呼叫該任務的 cancel")
	}
}
