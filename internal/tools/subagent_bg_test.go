package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingRunner 的 RunSub 阻塞到 release 關閉，用來測背景子 agent 的「執行中 → 完成」轉換。
type blockingRunner struct {
	release chan struct{}
	result  string
}

func (r *blockingRunner) RunSub(_ context.Context, _ SubTask) (string, error) {
	<-r.release
	return r.result, nil
}

func TestSubagentManager_BackgroundLifecycle(t *testing.T) {
	r := &blockingRunner{release: make(chan struct{}), result: "done-report"}
	m := NewSubagentManager(r)

	id, err := m.Spawn(SubTask{}, "explorer")
	if err != nil {
		t.Fatal(err)
	}
	if id != "bg-1" {
		t.Errorf("首個 ID 應為 bg-1，got %q", id)
	}
	// 釋放前：執行中
	if !strings.Contains(m.Result(id), "執行中") {
		t.Errorf("釋放前應為執行中，got %q", m.Result(id))
	}
	// 釋放 → 完成，結果可查
	close(r.release)
	waitFor(t, func() bool { return strings.Contains(m.Result(id), "已完成") }, "背景 sub 應轉為完成")
	if !strings.Contains(m.Result(id), "done-report") {
		t.Errorf("完成後應含結果，got %q", m.Result(id))
	}
	// 未知 ID
	if !strings.Contains(m.Result("bg-999"), "找不到") {
		t.Error("未知 ID 應提示找不到")
	}
	// list 含該 id
	if !strings.Contains(m.List(), id) {
		t.Error("List 應含該背景 sub")
	}
}

// perIDRunner 依 task.Prompt 決定要卡在哪個 channel，用來讓兩個背景 sub 各自獨立地結束。
type perIDRunner struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
}

func (r *perIDRunner) RunSub(_ context.Context, task SubTask) (string, error) {
	r.mu.Lock()
	ch := r.gates[task.Prompt]
	r.mu.Unlock()
	<-ch
	return "報告：" + task.Prompt, nil
}

// await 的重點不是「拿得到結果」（Result 就能拿），是【等待期間不燒錢】：
// 阻塞在工具呼叫裡，而不是讓模型每隔幾秒問一次 subagent_result。
func TestSubagentManager_Await(t *testing.T) {
	r := &perIDRunner{gates: map[string]chan struct{}{
		"fast": make(chan struct{}), "slow": make(chan struct{}),
	}}
	m := NewSubagentManager(r)
	fast, _ := m.Spawn(SubTask{Prompt: "fast"}, "A")
	slow, _ := m.Spawn(SubTask{Prompt: "slow"}, "B")

	// 任一模式：fast 一結束就該回來，不必等 slow
	go func() { time.Sleep(30 * time.Millisecond); close(r.gates["fast"]) }()
	start := time.Now()
	out := m.Await(context.Background(), []string{fast, slow}, false, 5*time.Second)
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("任一模式不該等到 slow：花了 %v", el)
	}
	if !strings.Contains(out, "報告：fast") {
		t.Errorf("應帶回已完成者的結果，got %q", out)
	}

	// 全部模式：slow 沒結束就得繼續等，逾時要回來而不是掛死
	start = time.Now()
	out = m.Await(context.Background(), []string{fast, slow}, true, 120*time.Millisecond)
	if el := time.Since(start); el < 100*time.Millisecond {
		t.Errorf("全部模式在 slow 未結束時不該立刻回來：只花了 %v", el)
	}
	if !strings.Contains(out, "仍未達標") {
		t.Errorf("逾時要講清楚它還在跑，got %q", out)
	}

	// 取消：立刻回來，而且【不能】把還在跑的子 agent 丟掉
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	start = time.Now()
	out = m.Await(ctx, []string{slow}, true, 5*time.Second)
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("取消後應立刻回來：花了 %v", el)
	}
	if !strings.Contains(out, "中止") {
		t.Errorf("取消要說明背景 sub 仍在跑，got %q", out)
	}
	close(r.gates["slow"])
	waitFor(t, func() bool { return strings.Contains(m.Result(slow), "已完成") },
		"取消等待不該影響背景子 agent 自己跑完")

	// 沒有執行中的：不要讓它傻等到逾時
	if out := m.Await(context.Background(), nil, false, time.Second); !strings.Contains(out, "不需要等待") {
		t.Errorf("全部都結束時該直接回來，got %q", out)
	}
}

func TestSubagentManager_ConcurrencyLimit(t *testing.T) {
	r := &blockingRunner{release: make(chan struct{})}
	m := NewSubagentManager(r)
	for i := 0; i < maxBackgroundSubagents; i++ {
		if _, err := m.Spawn(SubTask{}, "x"); err != nil {
			t.Fatalf("第 %d 個應成功: %v", i, err)
		}
	}
	if _, err := m.Spawn(SubTask{}, "x"); err == nil {
		t.Errorf("超過並發上限 %d 應回錯", maxBackgroundSubagents)
	}
	close(r.release) // 收尾，避免 goroutine 卡住
}
