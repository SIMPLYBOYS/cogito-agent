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
	b := &Core{busy: make(map[string]bool)}
	wdA := "/srv/ws/channels/A"
	wdB := "/srv/ws/channels/B"

	if !b.tryAcquire(wdA) {
		t.Fatal("首次應取得 A")
	}
	if b.tryAcquire(wdA) {
		t.Fatal("A 忙碌中，同目錄第二次應失敗（序列化）")
	}
	if !b.tryAcquire(wdB) {
		t.Fatal("不同目錄 B 應可並行取得")
	}

	b.release(wdA)
	if !b.tryAcquire(wdA) {
		t.Fatal("釋放後 A 應可再取得")
	}
}
