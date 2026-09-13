package tools

import (
	"fmt"
	"strings"
	"sync"
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

// 名額檢查與登記之間隔著 fork/exec（數 ms）。引擎同一輪會並行跑工具，兩個 bash_background
// 同時進來就能一起通過檢查——上限變成擺設。
func TestTaskManager_ConcurrencyLimitUnderRace(t *testing.T) {
	tm := NewTaskManager(sandbox.HostExecutor{}, t.TempDir())
	defer tm.KillAll()

	const callers = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	gate := make(chan struct{})
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			if _, err := tm.Start("sleep 30"); err == nil {
				mu.Lock()
				started++
				mu.Unlock()
			}
		}()
	}
	close(gate)
	wg.Wait()
	if started > MaxBackgroundTasks {
		t.Fatalf("並發上限 %d 被突破：%d 個呼叫同時進來，成功啟動 %d 個", MaxBackgroundTasks, callers, started)
	}
}

// 關機走 KillAll。它若只 cancel（只殺 bash），bash 底下的孫行程會活下來繼續佔著輸出管線與埠口，
// Wait 也就永遠等不到。KillAll 回來時，每個任務都必須真的結束了。
func TestTaskManager_KillAllReapsProcessTree(t *testing.T) {
	tm := NewTaskManager(sandbox.HostExecutor{}, t.TempDir())
	id, err := tm.Start("sleep 30; true") // 多一個 `; true`，bash 才會 fork 出 sleep 而不是直接 exec
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // 讓 bash 真的把 sleep 拉起來

	tm.KillAll()
	ts := tm.get(id)
	ts.mu.Lock()
	done := ts.done
	ts.mu.Unlock()
	if !done {
		t.Fatal("KillAll 回來時任務仍未結束：孫行程還活著、握著輸出管線")
	}
}
