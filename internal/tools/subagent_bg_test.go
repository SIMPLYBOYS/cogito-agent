package tools

import (
	"context"
	"strings"
	"testing"
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
