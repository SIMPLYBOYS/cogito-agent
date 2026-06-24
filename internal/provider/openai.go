package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// OpenAIProvider 是一個【OpenAI 相容】的 LLMProvider：手寫精簡的 /chat/completions 客戶端。
// 因為 OpenAI、本地 vLLM、Ollama、OpenRouter、Groq、Together… 都講同一套 chat-completions API，
// 一個可配 BaseURL 的 provider 就能接上「200+ 模型」，這正是補齊「Claude 單一」廣度缺口的關鍵。
type OpenAIProvider struct {
	cfg    OpenAIConfig
	client *http.Client
}

// OpenAIConfig 控制端點/金鑰/模型/窗口。零值欄位套用預設。
type OpenAIConfig struct {
	BaseURL          string // 預設 https://api.openai.com/v1
	APIKey           string
	Model            string // 預設 gpt-4o-mini
	MaxContextTokens int    // 預設 128000
	HTTPClient       *http.Client
}

func NewOpenAIProvider(cfg OpenAIConfig) *OpenAIProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = 128000
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &OpenAIProvider{cfg: cfg, client: client}
}

func (p *OpenAIProvider) ModelName() string     { return p.cfg.Model }
func (p *OpenAIProvider) MaxContextTokens() int { return p.cfg.MaxContextTokens }

// ---- wire types（OpenAI chat-completions）----

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON 字串
	} `json:"function"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

type oaiRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Tools    []oaiTool    `json:"tools,omitempty"`
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Content   string        `json:"content"`
			ToolCalls []oaiToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// toOpenAIMessages 把統一的 schema.Message 序列映射成 OpenAI 訊息。
// tool 結果（RoleUser + ToolCallID）→ role:tool；assistant 的 ToolCalls → tool_calls。
func toOpenAIMessages(msgs []schema.Message) []oaiMessage {
	out := make([]oaiMessage, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case schema.RoleSystem:
			out = append(out, oaiMessage{Role: "system", Content: m.Content})
		case schema.RoleUser:
			if m.ToolCallID != "" {
				out = append(out, oaiMessage{Role: "tool", ToolCallID: m.ToolCallID, Content: m.Content})
			} else {
				out = append(out, oaiMessage{Role: "user", Content: m.Content})
			}
		case schema.RoleAssistant:
			om := oaiMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				var c oaiToolCall
				c.ID = tc.ID
				c.Type = "function"
				c.Function.Name = tc.Name
				c.Function.Arguments = string(tc.Arguments)
				om.ToolCalls = append(om.ToolCalls, c)
			}
			out = append(out, om)
		}
	}
	return out
}

func toOpenAITools(tools []schema.ToolDefinition) []oaiTool {
	out := make([]oaiTool, 0, len(tools))
	for _, t := range tools {
		var ot oaiTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.InputSchema
		out = append(out, ot)
	}
	return out
}

func (p *OpenAIProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	if p.cfg.APIKey == "" {
		return nil, fmt.Errorf("缺少 OPENAI_API_KEY（OpenAI 相容 provider）")
	}

	reqBody := oaiRequest{Model: p.cfg.Model, Messages: toOpenAIMessages(msgs)}
	if len(availableTools) > 0 {
		reqBody.Tools = toOpenAITools(availableTools)
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化請求失敗: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OpenAI 相容 API 請求失敗: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var parsed oaiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析回應失敗（HTTP %d）: %w\n%s", resp.StatusCode, err, truncate(string(raw), 300))
	}
	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil {
			return nil, fmt.Errorf("OpenAI 相容 API 錯誤（HTTP %d）: %s", resp.StatusCode, parsed.Error.Message)
		}
		return nil, fmt.Errorf("OpenAI 相容 API 錯誤（HTTP %d）: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("回應沒有 choices")
	}

	choice := parsed.Choices[0].Message
	result := &schema.Message{Role: schema.RoleAssistant, Content: choice.Content}
	for _, tc := range choice.ToolCalls {
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		result.ToolCalls = append(result.ToolCalls, schema.ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(args),
		})
	}
	if parsed.Usage.PromptTokens > 0 || parsed.Usage.CompletionTokens > 0 {
		result.Usage = &schema.Usage{
			PromptTokens:     parsed.Usage.PromptTokens,
			CompletionTokens: parsed.Usage.CompletionTokens,
			CacheReadTokens:  parsed.Usage.PromptTokensDetails.CachedTokens,
		}
	}
	return result, nil
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
