package tools

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
)

const (
	// MaxBackgroundTasks 是單個 TaskManager（= 單 session）的背景任務並發上限——
	// memory「重啟條件」要求的並發上限，擋住 agent 無限拉 daemon 把機器吃爆。
	MaxBackgroundTasks = 5
	// maxTaskOutputBytes 是每個任務輸出緩衝的上限（保留尾部），避免噪音 server 撐爆記憶體。
	maxTaskOutputBytes = 256 * 1024
	// doneTaskRetention 是「已結束任務」的保留數：超出的最舊者在下次 Start 時清掉。
	// 沒有這層，tm.tasks 只增不減——長活 session 每跑一個背景任務就滯留一個 taskState（各含最多
	// 256KB buffer），單調洩漏。保留最近 N 個讓 List/Output 仍能查近況，記憶體上界 = N×256KB。
	doneTaskRetention = 10
)

// syncBuffer 是併發安全、有上限的輸出緩衝：背景行程 goroutine 寫、Output 工具讀。
// 超過上限時丟棄最舊的位元組（保留尾部，因為人通常看最新輸出）。
type syncBuffer struct {
	mu        sync.Mutex
	buf       []byte
	max       int
	truncated bool
}

func newSyncBuffer(max int) *syncBuffer { return &syncBuffer{max: max} }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
		b.truncated = true
	}
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := string(b.buf)
	if b.truncated {
		return "...[前段輸出過長已截斷]...\n" + s
	}
	return s
}

// taskState 是一個背景任務的完整狀態。done/exitErr 由 Wait goroutine 更新，受 mu 保護。
type taskState struct {
	id        string
	command   string
	startedAt time.Time
	buf       *syncBuffer
	cancel    context.CancelFunc

	mu      sync.Mutex
	done    bool
	killed  bool
	exitErr error
}

// TaskManager 是 session 級的背景任務池：用 sandbox.Executor 在【與前景 bash 同一隔離邊界】內
// 拉起長時間命令（不受 bash 工具 30s 同步 timeout 限制），可跨 Turn 查輸出 / 終止。
// memory 記的重啟條件全部對應：session 級作用域（每 session 一個 TM）、並發上限（MaxBackgroundTasks）、
// 拉起走同一危險審批（bash_background 併入黑名單）、結束統一 kill（KillAll，cmd 優雅關閉時呼叫）。
type TaskManager struct {
	mu       sync.Mutex
	tasks    map[string]*taskState
	executor sandbox.Executor
	workDir  string
	seq      int
}

func NewTaskManager(executor sandbox.Executor, workDir string) *TaskManager {
	return &TaskManager{tasks: make(map[string]*taskState), executor: executor, workDir: workDir}
}

// pruneDoneLocked 清掉「已結束且超出保留數」的最舊任務，只保留最近結束的 doneTaskRetention 個。
// 須在持有 tm.mu 時呼叫（鎖序 tm.mu → ts.mu，與 runningCount 一致）。
func (tm *TaskManager) pruneDoneLocked() {
	type done struct {
		id      string
		started time.Time
	}
	var finished []done
	for id, ts := range tm.tasks {
		ts.mu.Lock()
		d := ts.done
		ts.mu.Unlock()
		if d {
			finished = append(finished, done{id, ts.startedAt})
		}
	}
	if len(finished) <= doneTaskRetention {
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].started.Before(finished[j].started) })
	for _, f := range finished[:len(finished)-doneTaskRetention] {
		delete(tm.tasks, f.id)
	}
}

func (tm *TaskManager) runningCount() int {
	n := 0
	for _, t := range tm.tasks {
		t.mu.Lock()
		if !t.done {
			n++
		}
		t.mu.Unlock()
	}
	return n
}

// Start 在背景拉起一條命令，立即回傳任務 ID（不等待完成）。
func (tm *TaskManager) Start(command string) (string, error) {
	tm.mu.Lock()
	tm.pruneDoneLocked() // 順手清掉超出保留數的舊結束任務，防長活 session 的 map 單調洩漏
	if tm.runningCount() >= MaxBackgroundTasks {
		tm.mu.Unlock()
		return "", fmt.Errorf("背景任務已達並發上限 %d，請先用 task_kill 收掉不需要的任務", MaxBackgroundTasks)
	}
	tm.seq++
	id := fmt.Sprintf("task-%d", tm.seq)
	tm.mu.Unlock()

	// 刻意用獨立的可取消 context（非 bash 的 30s）——背景任務本來就要長活。
	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := tm.executor.Command(ctx, command, tm.workDir)
	if err != nil {
		cancel()
		return "", fmt.Errorf("建立背景命令失敗: %w", err)
	}
	buf := newSyncBuffer(maxTaskOutputBytes)
	cmd.Stdout = buf
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("啟動背景任務失敗: %w", err)
	}

	ts := &taskState{id: id, command: command, startedAt: time.Now(), buf: buf, cancel: cancel}
	tm.mu.Lock()
	tm.tasks[id] = ts
	tm.mu.Unlock()

	go func(c *exec.Cmd, st *taskState) {
		err := c.Wait()
		st.mu.Lock()
		st.done = true
		st.exitErr = err
		st.mu.Unlock()
	}(cmd, ts)

	return id, nil
}

func (tm *TaskManager) get(id string) *taskState {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.tasks[id]
}

// Output 回傳某任務目前累積的輸出 + 狀態（執行中 / 已結束 + 退出資訊）。
func (tm *TaskManager) Output(id string) (string, error) {
	ts := tm.get(id)
	if ts == nil {
		return "", fmt.Errorf("找不到背景任務 %q（用 task_list 看現有任務）", id)
	}
	ts.mu.Lock()
	done, killed, exitErr := ts.done, ts.killed, ts.exitErr
	ts.mu.Unlock()

	status := "🟢 執行中"
	switch {
	case killed:
		status = "🔴 已被終止"
	case done && exitErr != nil:
		status = fmt.Sprintf("⚪ 已結束（%v）", exitErr)
	case done:
		status = "✅ 已結束（退出碼 0）"
	}
	out := ts.buf.String()
	if out == "" {
		out = "（尚無輸出）"
	}
	return fmt.Sprintf("任務 %s [%s]\n指令: %s\n--- 輸出 ---\n%s", id, status, ts.command, out), nil
}

// Kill 終止一個背景任務（取消其 context → 行程被殺）。
func (tm *TaskManager) Kill(id string) error {
	ts := tm.get(id)
	if ts == nil {
		return fmt.Errorf("找不到背景任務 %q", id)
	}
	ts.mu.Lock()
	already := ts.done
	ts.killed = true
	ts.mu.Unlock()
	ts.cancel()
	if already {
		return fmt.Errorf("任務 %q 已結束，無需終止", id)
	}
	return nil
}

// List 列出所有任務及狀態（穩定排序）。
func (tm *TaskManager) List() string {
	tm.mu.Lock()
	ids := make([]string, 0, len(tm.tasks))
	for id := range tm.tasks {
		ids = append(ids, id)
	}
	tm.mu.Unlock()
	if len(ids) == 0 {
		return "目前沒有背景任務。"
	}
	sort.Strings(ids)
	var b []byte
	b = append(b, "背景任務：\n"...)
	for _, id := range ids {
		ts := tm.get(id)
		ts.mu.Lock()
		state := "🟢 執行中"
		if ts.killed {
			state = "🔴 已終止"
		} else if ts.done {
			state = "✅ 已結束"
		}
		ts.mu.Unlock()
		b = append(b, fmt.Sprintf("- %s [%s] %s\n", id, state, ts.command)...)
	}
	return string(b)
}

// KillAll 終止所有任務（cmd 優雅關閉時呼叫，避免殘留孤兒行程）。
func (tm *TaskManager) KillAll() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, ts := range tm.tasks {
		ts.mu.Lock()
		ts.killed = true
		ts.mu.Unlock()
		ts.cancel()
	}
}
