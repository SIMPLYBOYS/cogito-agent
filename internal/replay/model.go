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

	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
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

// Run 是一個 session 的完整執行流。同一個 session（如 operator chat）會累積多個任務，
// Tasks 把扁平的 turns 依任務切段——沒有這層，任務一多頁面就是一條不可摺疊的長流水。
type Run struct {
	ID          string
	Query       string  // 第一則 user 訊息（觸發這次 run 的任務）
	Turns       []*Turn // 扁平全量（列表頁計步數用）；渲染走 Tasks
	Tasks       []*Task
	HasSubagent bool
	Meta        Meta
}

// Task 是 session 裡的一段任務：一則 user 指令與它引出的所有 turns。渲染成獨立的可摺疊卡片。
type Task struct {
	Query     string
	Turns     []*Turn
	Steps     int      // 實質步數（不含系統提醒）
	CostUSD   float64  // 本任務逐步成本合計（0＝舊 transcript 無 usage 或模型未知，模板據此不顯示）
	Open      bool     // 只有最後一個任務預設展開——舊任務摺疊，頁面才不會隨任務數無限長
	Artifacts []string // 本任務寫過的檔案（write_file/edit_file 的 path，含子 agent；去重保序）——產物可發現性
}

// Turn 是主 agent 的一步：thinking + 若干 action（工具呼叫）；或最終回答；或一條系統提醒。
type Turn struct {
	Index       int // 只有 action/answer turn 有編號（每個任務內從 1 起）；Note turn 為 0
	Thinking    string
	Actions     []*Action
	FinalAnswer string // 該輪無 tool call ＝ 最終回答
	Note        string // 系統提醒（成本軟著陸 / 續跑等，夾在流程中的 user 訊息）
	Usage       *schema.Usage
	CostUSD     float64   // 本步成本（有 Usage 且知道模型才非 0）
	Fan         []*Action // 本輪的【委派動作】，單獨拉出來渲染成 fan-out 卡片（一節點→多分支）
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
	WritePath   string  // write_file/edit_file 的目標路徑（自 raw 參數抽出，供任務產出清單）
	SubTurns    []*Turn // 子 agent 內部逐輪（有落地 sub-history 時才有）
	SubModel    string  // 子 agent 實際用的模型（可能與主 agent 不同）
	SubCostUSD  float64 // 子 agent 內部逐步成本合計（fan-out 卡片的標頭數字）
	SubSteps    int     // 子 agent 實質步數
}

// Build 把 history 重建成 Run。sessWorkDir 非空時，會用 spawn_subagent 節點的 CallID 去
// <sessWorkDir>/subagents/<CallID>.json 把子 agent 的【內部】執行掛回（M2 深度）；空則只到委派層。
func Build(id string, history []schema.Message, meta Meta, sessWorkDir string) Run {
	turns, query, hasSub := reconstruct(history)
	// 逐步成本：有 usage 且知道模型才算得出來（舊 transcript 無 usage → 0 → 模板不顯示，
	// 不會出現誤導的 $0.0000）。公式與 tracker 記帳同源（observability.CostOf）。
	if meta.Model != "" {
		for _, t := range turns {
			if t.Usage != nil {
				t.CostUSD = observability.CostOf(meta.Model, *t.Usage)
			}
		}
	}
	run := Run{ID: id, Query: query, Turns: turns, Tasks: groupTasks(query, turns), HasSubagent: hasSub, Meta: meta}
	if sessWorkDir != "" {
		for _, t := range turns {
			for _, a := range t.Actions {
				if a.IsSubagent && a.CallID != "" {
					if sr, ok := LoadSubRun(sessWorkDir, a.CallID); ok {
						a.SubTurns, _, _ = reconstruct(sr.History)
						a.SubModel = sr.Model
						// 用【子 agent 自己的】模型算逐步成本——它常與主 agent 不同（如子 agent 走 opus）。
						// 模型未知就不算（寧可不顯示，不顯示錯的），與主樹同一守則。
						for _, st := range a.SubTurns {
							if sr.Model != "" && st.Usage != nil {
								st.CostUSD = observability.CostOf(sr.Model, *st.Usage)
							}
							if st.Note == "" {
								a.SubSteps++
							}
							a.SubCostUSD += st.CostUSD
						}
					}
				}
			}
		}
	}
	// fan-out：把委派動作從時間線 Actions 拉到 Fan，讓「一輪派出多個子 agent」渲染成扇形卡片。
	// 在 sub-run 掛回【之後】做，卡片標頭才有步數與成本。指標不變，byCall 對應仍有效。
	for _, t := range turns {
		var rest []*Action
		for _, a := range t.Actions {
			if a.IsSubagent {
				t.Fan = append(t.Fan, a)
			} else {
				rest = append(rest, a)
			}
		}
		t.Actions = rest
	}

	// 任務產出清單：收集本任務寫過的檔案（含子 agent 內部的寫入）。在 sub-run 掛回後才收，
	// implementer 類子 agent 的寫檔才看得到。Tasks 與 turns 共享 *Turn 指標，直接掃即可。
	for _, tk := range run.Tasks {
		tk.Artifacts = collectArtifacts(tk.Turns)
	}

	return run
}

// collectArtifacts 掃一段 turns（含 Fan 委派的子 agent 內部）收 write_file/edit_file 的目標路徑，
// 去重保序。失敗的寫入不特別排除——路徑仍指出「agent 動過哪裡」，誤列成本低。
func collectArtifacts(turns []*Turn) []string {
	seen := map[string]bool{}
	var out []string
	add := func(a *Action) {
		if a.WritePath != "" && !seen[a.WritePath] {
			seen[a.WritePath] = true
			out = append(out, a.WritePath)
		}
	}
	for _, t := range turns {
		for _, a := range t.Actions {
			add(a)
		}
		for _, f := range t.Fan {
			for _, st := range f.SubTurns {
				for _, a := range st.Actions {
					add(a)
				}
			}
		}
	}
	return out
}

// groupTasks 把扁平 turns 依任務切段。邊界判準【不猜文字內容】：一則 user 訊息（Note）只有在
// 「前一個實質 turn 已出最終回答」時才是新任務——因為 chat 以鎖序列化，新任務只可能在上一個
// run 結束後送進來；而 run 中途注入的 user 訊息（成本軟著陸、迴圈提醒）必然出現在最終回答之前，
// 留在原任務內當系統提醒。
//
// ponytail: run 沒有最終回答就結束（崩潰/取消/熔斷）時，下一個任務會被併進上一段——顯示層的
// 小瑕疵，不影響資料。要根治得在 history 標記任務邊界，等真的困擾再說。
func groupTasks(query string, turns []*Turn) []*Task {
	cur := &Task{Query: query}
	tasks := []*Task{cur}
	prevCompleted := false
	for _, t := range turns {
		if t.Note != "" {
			if prevCompleted {
				cur = &Task{Query: t.Note} // Note 升格為新任務的標題
				tasks = append(tasks, cur)
				prevCompleted = false
				continue
			}
			cur.Turns = append(cur.Turns, t) // run 中途的注入：留在原任務
			continue
		}
		cur.Turns = append(cur.Turns, t)
		prevCompleted = t.FinalAnswer != ""
	}
	for _, tk := range tasks {
		n := 0
		for _, t := range tk.Turns {
			if t.Note != "" {
				continue
			}
			n++
			t.Index = n // 任務內重新編號（原全域編號在摺疊分段後沒有意義）
			tk.CostUSD += t.CostUSD
		}
		tk.Steps = n
	}
	tasks[len(tasks)-1].Open = true
	return tasks
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
				if tc.Name == "write_file" || tc.Name == "edit_file" {
					a.WritePath = jsonField(tc.Arguments, "path") // Args 已截斷供顯示，產出清單要從 raw 抽
				}
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
