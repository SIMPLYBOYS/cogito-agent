package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
)

// 等待條件成立（背景任務是非同步的），逾時則 fail。
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("逾時等待: %s", msg)
}

func TestTaskManager_StartOutputFinish(t *testing.T) {
	tm := NewTaskManager(sandbox.HostExecutor{}, t.TempDir())
	id, err := tm.Start("echo hello-bg; exit 0")
	if err != nil {
		t.Fatalf("Start 失敗: %v", err)
	}

	waitFor(t, func() bool {
		out, _ := tm.Output(id)
		return strings.Contains(out, "hello-bg") && strings.Contains(out, "已結束")
	}, "任務應輸出 hello-bg 並結束")
}

func TestTaskManager_Kill(t *testing.T) {
	tm := NewTaskManager(sandbox.HostExecutor{}, t.TempDir())
	id, err := tm.Start("sleep 30") // 長命任務
	if err != nil {
		t.Fatal(err)
	}
	// 一開始應在執行中
	if out, _ := tm.Output(id); !strings.Contains(out, "執行中") {
		t.Errorf("剛啟動應為執行中: %s", out)
	}
	if err := tm.Kill(id); err != nil {
		t.Fatalf("Kill 失敗: %v", err)
	}
	waitFor(t, func() bool {
		out, _ := tm.Output(id)
		return strings.Contains(out, "已被終止")
	}, "Kill 後狀態應為已終止")
}

func TestTaskManager_ConcurrencyLimit(t *testing.T) {
	tm := NewTaskManager(sandbox.HostExecutor{}, t.TempDir())
	for i := range MaxBackgroundTasks {
		if _, err := tm.Start("sleep 30"); err != nil {
			t.Fatalf("第 %d 個任務不該失敗: %v", i, err)
		}
	}
	// 第 N+1 個應被並發上限擋下
	if _, err := tm.Start("sleep 30"); err == nil {
		t.Error("超過並發上限應回 error")
	}
	tm.KillAll()
}

func TestTaskManager_UnknownTask(t *testing.T) {
	tm := NewTaskManager(sandbox.HostExecutor{}, t.TempDir())
	if _, err := tm.Output("nope"); err == nil {
		t.Error("未知任務 Output 應回 error")
	}
	if err := tm.Kill("nope"); err == nil {
		t.Error("未知任務 Kill 應回 error")
	}
}

func TestSyncBuffer_Cap(t *testing.T) {
	b := newSyncBuffer(10)
	_, _ = b.Write([]byte("0123456789ABCDE")) // 15 bytes，超過上限 10
	s := b.String()
	if !strings.Contains(s, "截斷") {
		t.Errorf("超量應標記截斷: %q", s)
	}
	if !strings.Contains(s, "BCDE") {
		t.Errorf("應保留尾部: %q", s)
	}
}
