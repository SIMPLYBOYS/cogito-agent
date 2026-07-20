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
}

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// 若這是 Assistant 回覆，存放本次呼叫的 Token 消耗（請求時不發送）
	Usage *Usage `json:"usage,omitempty"`
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
}

type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}
