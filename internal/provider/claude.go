package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type ClaudeProvider struct {
	client anthropic.Client
	model  string
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

func (p *ClaudeProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
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

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: 4096,
		Messages:  anthropicMsgs,
	}

	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	if len(anthropicTools) > 0 {
		params.Tools = anthropicTools
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("Claude/Zhipu API 請求失敗: %w", err)
	}

	resultMsg := &schema.Message{
		Role: schema.RoleAssistant,
	}

	for _, block := range resp.Content {
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
	if resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0 {
		resultMsg.Usage = &schema.Usage{
			PromptTokens:     int(resp.Usage.InputTokens),
			CompletionTokens: int(resp.Usage.OutputTokens),
		}
	}

	return resultMsg, nil
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
