package chatbot

import (
	"path/filepath"
	"strings"
	"testing"
)

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
