package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/yourname/go-tiny-claw/internal/schema"
)

type ClaudeProvider struct {
	client anthropic.Client
	model  string
}

// NewClaudeProvider 走 Anthropic 官方端点（https://api.anthropic.com）。
// 不覆盖 baseURL，让 SDK 使用默认地址；凭证读取 ANTHROPIC_API_KEY。
// model 需传真实 Claude 模型 id，例如 "claude-opus-4-8"。
func NewClaudeProvider(model string) *ClaudeProvider {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		panic("请设置 ANTHROPIC_API_KEY 环境变量")
	}
	return &ClaudeProvider{
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:  model,
	}
}

func (p *ClaudeProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	anthropicMsgs, systemPrompt := buildAnthropicMessages(msgs)

	var anthropicTools []anthropic.ToolUnionParam
	for _, toolDef := range availableTools {
		// ToolInputSchemaParam 是结构体，需要通过 Properties 字段填充
		// InputSchema 里的 "properties" 值取出来赋给它
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
				// schema 来自 JSON 反序列化时，required 会是 []interface{}
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
		return nil, fmt.Errorf("Claude/Zhipu API 请求失败: %w", err)
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

	return resultMsg, nil
}

// buildAnthropicMessages 把统一的 schema.Message 历史转换为 Anthropic 的 MessageParam 序列，
// 并抽出 system prompt。核心职责是维持 Anthropic 要求的 user/assistant 严格交替不变式：
//   - 同一 assistant 回合触发的多个 tool_result 合并进同一条 user 消息；
//   - 紧跟 tool_result 之后的普通 user 文本（如 ch15 死循环提醒）并入同一条 user 消息（文本块），
//     避免「tool_result user + 文本 user」连续两条 user 被 Anthropic 拒绝。
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
	// 循环结束后把残留的工具结果 flush 掉（最后一条消息正好是 tool_result 的情况）
	flushToolResults()

	return anthropicMsgs, systemPrompt
}
