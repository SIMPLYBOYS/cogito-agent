// Package replay 把一次 query 的扁平對話歷史（session history）重建成結構化的「執行流」（run-tree）：
// 主 agent 的逐輪 thinking → action → observation，以及它委派了哪些 subagent。這是 operator dashboard
// 的 run 視覺化核心，也刻意與交付載體解耦——同一個 model 未來可渲染成靜態 HTML artifact。
//
// 邊界（M1）：只重建主 agent 迴圈 + subagent【委派節點 + 報告】；subagent 的【內部】turns 不在 session
// history 裡（RunSub 只回報告字串、不持久化），要等 M2 讓 RunSub 落地 sub-history 才畫得到。
package replay

import (
	"encoding/json"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// Meta 是一次 run 的元資料（來自 SessionSnapshot，由呼叫端填——replay 不依賴 session store，只吃 schema）。
type Meta struct {
	UpdatedAt        string
	Cost             float64
	PromptTokens     int
	CompletionTokens int
	Model            string
	Goal             string
	Running          bool
}

// Run 是一次 query 的完整執行流。
type Run struct {
	ID          string
	Query       string // 第一則 user 訊息（觸發這次 run 的任務）
	Turns       []*Turn
	HasSubagent bool
	Meta        Meta
}

// Turn 是主 agent 的一步：thinking + 若干 action（工具呼叫）；或最終回答；或一條系統提醒。
type Turn struct {
	Index       int // 只有 action/answer turn 有編號（從 1 起）；Note turn 為 0
	Thinking    string
	Actions     []*Action
	FinalAnswer string // 該輪無 tool call ＝ 最終回答
	Note        string // 系統提醒（成本軟著陸 / 續跑等，夾在流程中的 user 訊息）
	Usage       *schema.Usage
}

// Action 是一次工具呼叫。IsSubagent 時 AgentType/Report 有值（Report 即該子 agent 的回報）；
// SubTurns 為該子 agent 的【內部】執行（M2：從 subagents/<CallID>.json 掛回，可展開）。
type Action struct {
	Tool        string
	Args        string
	Observation string
	IsSubagent  bool
	AgentType   string
	Report      string
	CallID      string  // ToolCall.ID——用來找 subagents/<CallID>.json
	SubTurns    []*Turn // 子 agent 內部逐輪（有落地 sub-history 時才有）
}

// Build 把 history 重建成 Run。sessWorkDir 非空時，會用 spawn_subagent 節點的 CallID 去
// <sessWorkDir>/subagents/<CallID>.json 把子 agent 的【內部】執行掛回（M2 深度）；空則只到委派層。
func Build(id string, history []schema.Message, meta Meta, sessWorkDir string) Run {
	turns, query, hasSub := reconstruct(history)
	run := Run{ID: id, Query: query, Turns: turns, HasSubagent: hasSub, Meta: meta}
	if sessWorkDir != "" {
		for _, t := range turns {
			for _, a := range t.Actions {
				if a.IsSubagent && a.CallID != "" {
					if sr, ok := LoadSubRun(sessWorkDir, a.CallID); ok {
						a.SubTurns, _, _ = reconstruct(sr.History)
					}
				}
			}
		}
	}
	return run
}

// reconstruct 把一段扁平 history 重建成 turns（+ query + 是否有 subagent）。主 run 與 subagent 內部共用它。
// 演算法：assistant 開一輪（thinking + actions）；user+ToolCallID 是工具結果（回填對應 action 的
// observation）；第一則 user（無 ToolCallID）是 query，其後的當系統提醒。
func reconstruct(history []schema.Message) (turns []*Turn, query string, hasSub bool) {
	byCall := map[string]*Action{} // ToolCall.ID → *Action（指標，避免 slice 擴張失聯）
	for _, m := range history {
		switch m.Role {
		case schema.RoleSystem:
			continue // 靜態系統層，不入 run-tree
		case schema.RoleUser:
			if m.ToolCallID != "" { // 工具結果 → 回填觀察
				if a := byCall[m.ToolCallID]; a != nil {
					a.Observation = m.Content
					if a.IsSubagent {
						a.Report = m.Content
					}
				}
				continue
			}
			if query == "" {
				query = m.Content // 首則 user ＝ 任務
			} else {
				turns = append(turns, &Turn{Note: m.Content}) // 系統提醒/續跑
			}
		case schema.RoleAssistant:
			t := &Turn{Thinking: m.Content, Usage: m.Usage}
			for _, tc := range m.ToolCalls {
				a := &Action{Tool: tc.Name, Args: prettyArgs(tc.Arguments), CallID: tc.ID}
				if tc.Name == "spawn_subagent" {
					a.IsSubagent = true
					hasSub = true
					if at := jsonField(tc.Arguments, "agent_type"); at != "" {
						a.AgentType = at
					} else {
						a.AgentType = "探路者（預設）"
					}
				}
				t.Actions = append(t.Actions, a)
				byCall[tc.ID] = a
			}
			if len(m.ToolCalls) == 0 { // 無工具 ＝ 最終回答
				t.FinalAnswer = m.Content
				t.Thinking = ""
			}
			turns = append(turns, t)
		}
	}
	n := 0
	for _, t := range turns {
		if t.Note == "" {
			n++
			t.Index = n
		}
	}
	return turns, query, hasSub
}

// prettyArgs 把工具參數（json.RawMessage）壓成單行、截斷的可讀字串（供顯示，非結構化）。
func prettyArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return schema.TruncRunes(strings.TrimSpace(string(raw)), 200, "…")
	}
	b, err := json.Marshal(v) // 壓掉多餘空白
	if err != nil {
		return schema.TruncRunes(strings.TrimSpace(string(raw)), 200, "…")
	}
	return schema.TruncRunes(string(b), 200, "…")
}

// jsonField 從 json object 取一個字串欄位（取不到回空）。
func jsonField(raw json.RawMessage, key string) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}
