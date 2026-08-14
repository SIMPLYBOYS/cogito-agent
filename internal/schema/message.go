package schema

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Usage 記錄單次大模型 API 呼叫的 Token 消耗。
type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`         // 輸入 Token 數
	CompletionTokens    int `json:"completion_tokens"`     // 輸出 Token 數
	CacheReadTokens     int `json:"cache_read_tokens"`     // 命中 prompt cache 的輸入 Token（約 0.1x 計費）
	CacheCreationTokens int `json:"cache_creation_tokens"` // 寫入 prompt cache 的輸入 Token（約 1.25x 計費）
	// LatencyMS 是這次 API 呼叫的耗時。CostTracker 本來就量了它，但只印進 log 就丟掉——
	// 於是「哪一輪突然變慢」在任何介面上都查不到。omitempty：舊 session 沒有這個欄位，讀得動。
	LatencyMS int64 `json:"latency_ms,omitempty"`
}

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// 若這是 Assistant 回覆，存放本次呼叫的 Token 消耗（請求時不發送）
	Usage *Usage `json:"usage,omitempty"`
	// TS 是這則訊息進入歷史的時間（RFC3339）。沒有它就【做不出任何時間序列】——
	// 成本趨勢、延遲分佈、「哪一輪開始變慢」全都要先有時間軸。由 Session.Append 統一蓋章。
	//
	// 加在這裡對 API 請求無影響：兩家 provider 都是把 schema.Message【轉換】成自己的型別
	// （buildAnthropicMessages / toOpenAIMessages），不是直接序列化這個結構，所以
	// prompt cache 的前綴不會因為多一個欄位而失效。
	TS string `json:"ts,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`
	// Denied：政策拒絕（Deny／無人值守 fail-closed）。與一般 IsError 的語意差異是引擎的處理：
	// 一般錯誤是「可重試的觀察」（附救援指南餵回迴圈），政策拒絕是【該目標的終止】——實測
	// （docs/incident-blacklist-bypass.md）agent 會把拒絕當「換個方法再試」的回饋而主動繞過黑名單。
	Denied bool `json:"denied,omitempty"`
}

type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}
