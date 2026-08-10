package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

const (
	maxBackgroundSubagents  = 3  // 單 session 背景子 agent 並發上限（擋住無限背景 fan-out）
	subagentResultRetention = 10 // 已結束保留數（供查詢）；更舊的在下次 Spawn 時清掉，防 map 洩漏
)

type bgSubState struct {
	id        string
	label     string // agent 角色（顯示用）
	startedAt time.Time
	finished  chan struct{} // 結束時 close：讓 subagent_await 能【阻塞】等，而不是靠模型輪詢

	mu     sync.Mutex
	done   bool
	result string
	err    error
}

// SubagentManager 是 session 級的背景子 agent 池：把 spawn_subagent(background=true) 的委派丟 goroutine
// 跑、存結果，供 subagent_result / subagent_list 查詢。對齊 TaskManager（背景 bash）的設計。
type SubagentManager struct {
	mu     sync.Mutex
	subs   map[string]*bgSubState
	seq    int
	runner AgentRunner
}

func NewSubagentManager(runner AgentRunner) *SubagentManager {
	return &SubagentManager{subs: make(map[string]*bgSubState), runner: runner}
}

func (m *SubagentManager) runningCount() int {
	n := 0
	for _, s := range m.subs {
		s.mu.Lock()
		if !s.done {
			n++
		}
		s.mu.Unlock()
	}
	return n
}

// pruneDoneLocked 清掉超出保留數的最舊【已結束】子 agent。須持 m.mu（鎖序 m.mu → s.mu）。
func (m *SubagentManager) pruneDoneLocked() {
	type done struct {
		id      string
		started time.Time
	}
	var finished []done
	for id, s := range m.subs {
		s.mu.Lock()
		d := s.done
		s.mu.Unlock()
		if d {
			finished = append(finished, done{id, s.startedAt})
		}
	}
	if len(finished) <= subagentResultRetention {
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].started.Before(finished[j].started) })
	for _, f := range finished[:len(finished)-subagentResultRetention] {
		delete(m.subs, f.id)
	}
}

// Spawn 在背景跑一個子 agent（task 已含工具/model/prompt；Reporter 應為 nil＝silent），立即回傳 ID。
func (m *SubagentManager) Spawn(task SubTask, label string) (string, error) {
	m.mu.Lock()
	m.pruneDoneLocked()
	if m.runningCount() >= maxBackgroundSubagents {
		m.mu.Unlock()
		return "", fmt.Errorf("背景子 agent 已達並發上限 %d，請先用 subagent_result 收掉已完成的", maxBackgroundSubagents)
	}
	m.seq++
	id := fmt.Sprintf("bg-%d", m.seq)
	st := &bgSubState{id: id, label: label, startedAt: time.Now(), finished: make(chan struct{})}
	m.subs[id] = st
	m.mu.Unlock()

	go func() {
		result, err := m.runner.RunSub(context.Background(), task) // 背景：獨立 context、不受主任務取消影響
		st.mu.Lock()
		st.done, st.result, st.err = true, result, err
		st.mu.Unlock()
		close(st.finished) // 一定要在設好狀態【之後】：醒來的人讀到的必須是完成後的值
	}()
	return id, nil
}

func (m *SubagentManager) get(id string) *bgSubState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subs[id]
}

// Result 回傳某背景子 agent 的狀態/結果。
func (m *SubagentManager) Result(id string) string {
	st := m.get(id)
	if st == nil {
		return fmt.Sprintf("找不到背景子 agent %q（用 subagent_list 看現有的）", id)
	}
	st.mu.Lock()
	done, result, err := st.done, st.result, st.err
	st.mu.Unlock()
	switch {
	case !done:
		return fmt.Sprintf("背景子 agent %s [%s]：🟢 執行中，尚無結果。", id, st.label)
	case err != nil:
		return fmt.Sprintf("背景子 agent %s [%s]：⚪ 已結束（失敗：%v）", id, st.label, err)
	default:
		return fmt.Sprintf("背景子 agent %s [%s]：✅ 已完成\n%s", id, st.label, result)
	}
}

// List 列出所有背景子 agent 及狀態（穩定排序）。
func (m *SubagentManager) List() string {
	m.mu.Lock()
	ids := make([]string, 0, len(m.subs))
	for id := range m.subs {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	if len(ids) == 0 {
		return "目前沒有背景子 agent。"
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("背景子 agent：\n")
	for _, id := range ids {
		st := m.get(id)
		st.mu.Lock()
		state := "🟢 執行中"
		if st.done {
			state = "✅ 已完成"
			if st.err != nil {
				state = "⚪ 失敗"
			}
		}
		st.mu.Unlock()
		fmt.Fprintf(&b, "- %s [%s] %s\n", id, state, st.label)
	}
	return b.String()
}

const (
	defaultAwaitTimeout = 5 * time.Minute
	maxAwaitTimeout     = 30 * time.Minute
)

// Await 阻塞等到背景子 agent 結束，回傳已完成者的結果。
//
// 這是為了取代「模型迴圈裡輪詢 subagent_result」：輪詢的每一輪都是一次 API 呼叫，等十分鐘
// 就是幾十次無意義的花費，而且中間那些回合什麼事都沒做。等待改成【一次阻塞的工具呼叫】之後，
// 等待期間零 token——代價只是一個閒置的 goroutine。
//
// all=false（預設）：任何一個結束就回來。逐張推進用——一有人交件就去看還有誰的相依滿足了。
// all=true：這批全部結束才回來。階段收斂用（六個人的意見收齊才動筆）。
//
// 呼叫端取消（/stop、任務逾時）會立刻回來；背景子 agent 本身不受影響，仍會跑完，
// 結果之後照樣查得到——這是刻意的：等不下去不等於要把人家做到一半的工作丟掉。
func (m *SubagentManager) Await(ctx context.Context, ids []string, all bool, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = defaultAwaitTimeout
	}
	if timeout > maxAwaitTimeout {
		timeout = maxAwaitTimeout
	}
	if len(ids) == 0 { // 沒指名＝等目前所有還在跑的
		m.mu.Lock()
		for id, s := range m.subs {
			s.mu.Lock()
			if !s.done {
				ids = append(ids, id)
			}
			s.mu.Unlock()
		}
		m.mu.Unlock()
		sort.Strings(ids)
	}
	if len(ids) == 0 {
		return "目前沒有執行中的背景子 agent，不需要等待。"
	}

	var waiting []*bgSubState
	for _, id := range ids {
		st := m.get(id)
		if st == nil {
			return fmt.Sprintf("找不到背景子 agent %q（用 subagent_list 看現有的）", id)
		}
		waiting = append(waiting, st)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		pending := 0
		for _, st := range waiting {
			select {
			case <-st.finished:
			default:
				pending++
			}
		}
		if pending == 0 || (!all && pending < len(waiting)) {
			return m.awaitReport(ids, "")
		}
		// 還沒達標：睡在「任何一個結束」上。all 模式下醒來會再繞一圈檢查剩下的。
		cases := make([]<-chan struct{}, 0, len(waiting))
		for _, st := range waiting {
			cases = append(cases, st.finished)
		}
		select {
		case <-ctx.Done():
			return m.awaitReport(ids, "（等待被中止，背景子 agent 仍在跑，稍後可用 subagent_result 查）")
		case <-timer.C:
			return m.awaitReport(ids, fmt.Sprintf("（等了 %s 仍未達標，背景子 agent 仍在跑，可再 await 或改用 subagent_result）", timeout))
		case <-anyOf(cases):
		}
	}
}

// anyOf 回傳一個「任一輸入 channel 關閉就關閉」的 channel。
func anyOf(chans []<-chan struct{}) <-chan struct{} {
	out := make(chan struct{})
	var once sync.Once
	for _, c := range chans {
		go func(c <-chan struct{}) {
			<-c
			once.Do(func() { close(out) })
		}(c)
	}
	return out
}

func (m *SubagentManager) awaitReport(ids []string, note string) string {
	var b strings.Builder
	if note != "" {
		b.WriteString(note + "\n")
	}
	for _, id := range ids {
		b.WriteString(m.Result(id) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Tools 回傳背景子 agent 的查詢工具（與 spawn_subagent 共用同一 manager）。
func (m *SubagentManager) Tools() []BaseTool {
	return []BaseTool{&subagentResultTool{m: m}, &subagentListTool{m: m}, &subagentAwaitTool{m: m}}
}

type subagentAwaitTool struct{ m *SubagentManager }

func (t *subagentAwaitTool) Name() string { return "subagent_await" }
func (t *subagentAwaitTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "【等】背景子 agent 做完並直接拿到結果。要等人交件時用這個，不要反覆呼叫 " +
			"subagent_result 輪詢——那每一輪都是一次額外的花費，這個工具等待期間不花錢。" +
			"預設任何一個結束就回來（適合一有人交件就繼續推進）；wait_all=true 則等這批全部結束。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "要等的背景子 agent ID（如 [\"bg-1\",\"bg-2\"]）；省略＝等目前所有執行中的。",
				},
				"wait_all": map[string]any{
					"type": "boolean",
					"description": "true＝全部結束才回來；預設 false＝任一結束就回來。",
				},
				"timeout_seconds": map[string]any{
					"type": "number",
					"description": "等待上限秒數（預設 300、上限 1800）。逾時不會殺掉子 agent，只是先回來。",
				},
			},
		},
	}
}
func (t *subagentAwaitTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		IDs     []string `json:"ids"`
		WaitAll bool     `json:"wait_all"`
		Timeout float64  `json:"timeout_seconds"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("參數解析失敗: %w", err)
		}
	}
	return t.m.Await(ctx, in.IDs, in.WaitAll, time.Duration(in.Timeout*float64(time.Second))), nil
}

type subagentResultTool struct{ m *SubagentManager }

func (t *subagentResultTool) Name() string { return "subagent_result" }
func (t *subagentResultTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "查看某個【背景】子 agent 的狀態與結果（spawn_subagent 帶 background=true 時回傳的 ID，如 bg-1）。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "背景子 agent 的 ID（如 bg-1）"},
			},
			"required": []string{"id"},
		},
	}
}
func (t *subagentResultTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}
	return t.m.Result(in.ID), nil
}

type subagentListTool struct{ m *SubagentManager }

func (t *subagentListTool) Name() string { return "subagent_list" }
func (t *subagentListTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "列出所有【背景】子 agent 及其狀態（執行中 / 已完成 / 失敗）。",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
}
func (t *subagentListTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return t.m.List(), nil
}
