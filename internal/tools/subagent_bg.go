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
	st := &bgSubState{id: id, label: label, startedAt: time.Now()}
	m.subs[id] = st
	m.mu.Unlock()

	go func() {
		result, err := m.runner.RunSub(context.Background(), task) // 背景：獨立 context、不受主任務取消影響
		st.mu.Lock()
		st.done, st.result, st.err = true, result, err
		st.mu.Unlock()
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

// Tools 回傳背景子 agent 的查詢工具（與 spawn_subagent 共用同一 manager）。
func (m *SubagentManager) Tools() []BaseTool {
	return []BaseTool{&subagentResultTool{m: m}, &subagentListTool{m: m}}
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
