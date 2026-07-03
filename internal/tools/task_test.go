package tools

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
)

// pruneDoneLocked 只保留最近 doneTaskRetention 個結束任務，最舊者被清；執行中任務不動。
func TestTaskManager_PruneDoneTasks(t *testing.T) {
	tm := NewTaskManager(nil, "/tmp")
	base := time.Now()
	for i := 0; i < doneTaskRetention+5; i++ {
		id := fmt.Sprintf("task-%d", i)
		tm.tasks[id] = &taskState{id: id, startedAt: base.Add(time.Duration(i) * time.Second), done: true}
	}
	tm.tasks["running"] = &taskState{id: "running", startedAt: base, done: false}

	tm.mu.Lock()
	tm.pruneDoneLocked()
	tm.mu.Unlock()

	if tm.get("running") == nil {
		t.Fatal("執行中任務不該被清掉")
	}
	if tm.get("task-0") != nil {
		t.Fatal("最舊的結束任務應被清掉")
	}
	done := 0
	for _, ts := range tm.tasks {
		if ts.done {
			done++
		}
	}
	if done != doneTaskRetention {
		t.Fatalf("結束任務應保留 %d 個，got %d", doneTaskRetention, done)
	}
}

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
