package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type ClaudeProvider struct {
	client    anthropic.Client
	model     string
	maxTokens int // 0＝用預設 4096；供 effort 調整子 agent 輸出上限
}

// NewClaudeProvider 走 Anthropic 官方端點（https://api.anthropic.com）。
// 不覆蓋 baseURL，讓 SDK 使用默認地址；憑證讀取 ANTHROPIC_API_KEY。
// model 需傳真實 Claude 模型 id，例如 "claude-opus-4-8"。
func NewClaudeProvider(model string) *ClaudeProvider {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		panic("請設置 ANTHROPIC_API_KEY 環境變量")
	}
	return &ClaudeProvider{
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:  model,
	}
}

// MaxContextTokens 返回 Claude 模型的上下文窗口。當前 Claude 家族（Opus/Sonnet/Haiku 4.x）
// 標準窗口均為 200k tokens。
func (p *ClaudeProvider) MaxContextTokens() int {
	return 200000
}

// ModelName 返回構造時傳入的 Claude 模型 id（如 claude-opus-4-8）。
func (p *ClaudeProvider) ModelName() string {
	return p.model
}

// Configure 回傳換了 model / maxTokens 的變體（值拷貝，原 provider 不變；共用同一 client）。
func (p *ClaudeProvider) Configure(model string, maxTokens int) LLMProvider {
	np := *p
	if model != "" {
		np.model = model
	}
	if maxTokens > 0 {
		np.maxTokens = maxTokens
	}
	return &np
}

func (p *ClaudeProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	resp, err := p.client.Messages.New(ctx, p.buildParams(msgs, availableTools))
	if err != nil {
		return nil, fmt.Errorf("Claude/Zhipu API 請求失敗: %w", err)
	}
	return extractMessage(resp.Content, resp.Usage), nil
}

// GenerateStream 與 Generate 等價，但走 Anthropic 的 SSE 串流：邊接收邊把「文字」token 增量餵給 onDelta
// （供 UI 逐字顯示），最後把整段回應（含 tool calls / Usage）組裝成 Message 回傳——引擎主迴圈邏輯不變。
// tool_use 的參數以 JSON 增量串流，由 SDK 的 Accumulate 累積，不進 onDelta（onDelta 只吐純文字）。
func (p *ClaudeProvider) GenerateStream(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition, onDelta func(string)) (*schema.Message, error) {
	stream := p.client.Messages.NewStreaming(ctx, p.buildParams(msgs, availableTools))
	var acc anthropic.Message
	for stream.Next() {
		event := stream.Current()
		if err := acc.Accumulate(event); err != nil {
			return nil, fmt.Errorf("Claude 串流累積失敗: %w", err)
		}
		if ev, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			if td, ok := ev.Delta.AsAny().(anthropic.TextDelta); ok && td.Text != "" && onDelta != nil {
				onDelta(td.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("Claude 串流請求失敗: %w", err)
	}
	return extractMessage(acc.Content, acc.Usage), nil
}

// buildParams 把統一歷史 + 工具定義組成 Anthropic 請求參數（含 prompt caching 斷點）。Generate 與
// GenerateStream 共用，確保串流與非串流的請求構造完全一致。
func (p *ClaudeProvider) buildParams(msgs []schema.Message, availableTools []schema.ToolDefinition) anthropic.MessageNewParams {
	anthropicMsgs, systemPrompt := buildAnthropicMessages(msgs)

	var anthropicTools []anthropic.ToolUnionParam
	for _, toolDef := range availableTools {
		// ToolInputSchemaParam 是結構體，需要通過 Properties 字段填充
		// InputSchema 裡的 "properties" 值取出來賦給它
		var properties map[string]any
		var required []string

		if m, ok := toolDef.InputSchema.(map[string]interface{}); ok {
			if p, ok := m["properties"].(map[string]interface{}); ok {
				properties = p
			}
			switch r := m["required"].(type) {
			case []string:
				required = r
			case []interface{}:
				// schema 來自 JSON 反序列化時，required 會是 []interface{}
				for _, v := range r {
					if s, ok := v.(string); ok {
						required = append(required, s)
					}
				}
			}
		}

		tp := anthropic.ToolParam{
			Name:        toolDef.Name,
			Description: anthropic.String(toolDef.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: properties,
				Required:   required,
			},
		}
		anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{OfTool: &tp})
	}

	maxTokens := int64(4096)
	if p.maxTokens > 0 {
		maxTokens = int64(p.maxTokens)
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: maxTokens,
		Messages:  anthropicMsgs,
	}

	// Prompt caching：系統提示與工具列表是同一 session 多輪間不變的前綴，標上 ephemeral 快取
	// 斷點後，後續輪次該前綴約 1 折計費（5 分鐘 TTL）。在「最後一個工具」與「系統提示」各設一個
	// 斷點即可快取整個 tools+system 前綴。
	if len(anthropicTools) > 0 {
		if last := anthropicTools[len(anthropicTools)-1].OfTool; last != nil {
			last.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		params.Tools = anthropicTools
	}

	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt, CacheControl: anthropic.NewCacheControlEphemeralParam()},
		}
	}

	return params
}

// extractMessage 把 Anthropic 回應的 content blocks + usage 轉成統一的 schema.Message。Generate（一次
// 回傳的 resp）與 GenerateStream（Accumulate 出的 message）內容結構相同，故共用。
func extractMessage(content []anthropic.ContentBlockUnion, usage anthropic.Usage) *schema.Message {
	resultMsg := &schema.Message{Role: schema.RoleAssistant}

	for _, block := range content {
		switch block.Type {
		case "text":
			resultMsg.Content += block.Text
		case "tool_use":
			argsBytes, _ := json.Marshal(block.Input)
			resultMsg.ToolCalls = append(resultMsg.ToolCalls, schema.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: argsBytes,
			})
		}
	}

	// 提取 Token 消耗（Anthropic 用 Input/OutputTokens 命名），供 CostTracker 計費
	if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheReadInputTokens > 0 {
		resultMsg.Usage = &schema.Usage{
			PromptTokens:        int(usage.InputTokens),
			CompletionTokens:    int(usage.OutputTokens),
			CacheReadTokens:     int(usage.CacheReadInputTokens),
			CacheCreationTokens: int(usage.CacheCreationInputTokens),
		}
	}

	return resultMsg
}

// buildAnthropicMessages 把統一的 schema.Message 歷史轉換為 Anthropic 的 MessageParam 序列，
// 並抽出 system prompt。核心職責是維持 Anthropic 要求的 user/assistant 嚴格交替不變式：
//   - 同一 assistant 回合觸發的多個 tool_result 合併進同一條 user 消息；
//   - 緊跟 tool_result 之後的普通 user 文本（如死循環提醒）併入同一條 user 消息（文本塊），
//     避免「tool_result user + 文本 user」連續兩條 user 被 Anthropic 拒絕。
func buildAnthropicMessages(msgs []schema.Message) ([]anthropic.MessageParam, string) {
	var anthropicMsgs []anthropic.MessageParam
	var systemPrompt string

	var pendingToolResults []anthropic.ContentBlockParamUnion
	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(pendingToolResults...))
			pendingToolResults = nil
		}
	}

	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			systemPrompt = msg.Content
		case schema.RoleUser:
			if msg.ToolCallID != "" {
				pendingToolResults = append(pendingToolResults,
					anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false))
			} else if len(pendingToolResults) > 0 {
				pendingToolResults = append(pendingToolResults, anthropic.NewTextBlock(msg.Content))
			} else {
				flushToolResults()
				anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(
					anthropic.NewTextBlock(msg.Content),
				))
			}
		case schema.RoleAssistant:
			flushToolResults()
			var blocks []anthropic.ContentBlockParamUnion
			if msg.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
			}
			for _, tc := range msg.ToolCalls {
				var inputMap map[string]interface{}
				_ = json.Unmarshal(tc.Arguments, &inputMap)
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfToolUse: &anthropic.ToolUseBlockParam{
						ID:    tc.ID,
						Name:  tc.Name,
						Input: inputMap,
					},
				})
			}
			if len(blocks) > 0 {
				anthropicMsgs = append(anthropicMsgs, anthropic.NewAssistantMessage(blocks...))
			}
		}
	}
	// 循環結束後把殘留的工具結果 flush 掉（最後一條消息正好是 tool_result 的情況）
	flushToolResults()

	return anthropicMsgs, systemPrompt
}
