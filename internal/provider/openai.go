package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

const (
	// maxRetries 是瞬時性失敗（網路錯誤 / 429 / 5xx）的重試次數。Claude 走官方 SDK 自帶重試，
	// 這裡替手寫的 OpenAI 相容路徑補上對等韌性——否則一次 rate limit 就整個任務中止。
	maxRetries = 3
	// maxBackoff 封頂單次退避，避免伺服器回一個巨大的 Retry-After 把任務卡死。
	maxBackoff = 30 * time.Second
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
	// ReasoningEffort 非空才送 reasoning_effort（none/low/medium/high…）；空＝不送、由端點決定。
	// 不能預設送：非推理模型與多數本地端點收到這個欄位會拒收。
	ReasoningEffort string
	// MaxTokens >0 才送 max_completion_tokens（具名 agent 的 effort 經 Configure 傳入）；0＝由端點決定。
	// 用 max_completion_tokens 而非 max_tokens：OpenAI 推理模型已不收 max_tokens。
	MaxTokens  int
	HTTPClient *http.Client
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

// Configure 回傳換了 model / 輸出上限的變體（沿用同一端點/金鑰/HTTP client）。
//
// 指定 claude- 模型（內建審查類具名 agent 都寫 claude-opus-4-8）：有 ANTHROPIC_API_KEY 就改走 Claude；
// 沒有就沿用本端點的模型——把 claude id 送去別家只會換來一次必然失敗的呼叫，而 NewClaudeProvider
// 缺金鑰會 panic。
func (p *OpenAIProvider) Configure(model string, maxTokens int) LLMProvider {
	if isClaudeModel(model) {
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			return NewClaudeProvider(model).Configure("", maxTokens)
		}
		log.Printf("[OpenAI] 未設 ANTHROPIC_API_KEY，忽略模型 %q、沿用 %s", model, p.cfg.Model)
		model = ""
	}
	cfg := p.cfg // 值拷貝（含 HTTPClient 指標，沿用同一 client）
	if model != "" {
		cfg.Model = model
	}
	if maxTokens > 0 {
		cfg.MaxTokens = maxTokens
	}
	return NewOpenAIProvider(cfg)
}

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
	Model               string            `json:"model"`
	Messages            []oaiMessage      `json:"messages"`
	Tools               []oaiTool         `json:"tools,omitempty"`
	ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	StreamOptions       *oaiStreamOptions `json:"stream_options,omitempty"`
}

type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaiUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type oaiError struct {
	Message string `json:"message"`
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Content   string        `json:"content"`
			ToolCalls []oaiToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage oaiUsage  `json:"usage"`
	Error *oaiError `json:"error"`
}

// oaiStreamChunk 是串流的一個 SSE 片段。tool_calls 以 index 分片傳送：id/name 通常在第一片，
// arguments 分散在後續各片，要依 index 拼接。
type oaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *oaiUsage `json:"usage"`
	Error *oaiError `json:"error"`
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
	body, err := p.requestBody(msgs, availableTools, false)
	if err != nil {
		return nil, err
	}
	resp, err := p.send(ctx, body)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("讀取回應失敗: %w", err)
	}

	var parsed oaiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析回應失敗（HTTP 200）: %w\n%s", err, truncate(string(raw), 300))
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("回應沒有 choices")
	}

	choice := parsed.Choices[0].Message
	result := &schema.Message{Role: schema.RoleAssistant, Content: choice.Content}
	for _, tc := range choice.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, toolCall(tc.ID, tc.Function.Name, tc.Function.Arguments))
	}
	result.Usage = toUsage(parsed.Usage)
	return result, nil
}

// GenerateStream 走 SSE 串流：文字增量即時餵給 onDelta（供面板 chat 逐字顯示），tool call 依 index
// 拼接分片參數，結束時組成與 Generate 同形的完整 Message（含 usage），引擎主迴圈邏輯不變。
// ponytail: 需要端點支援 stream_options.include_usage（OpenAI 官方支援）；不支援的相容端點會回 400，
// 屆時再為它關掉。不送就拿不到 usage，成本熔斷與壓縮校準會默默失效，所以不預設省略。
func (p *OpenAIProvider) GenerateStream(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition, onDelta func(string)) (*schema.Message, error) {
	body, err := p.requestBody(msgs, availableTools, true)
	if err != nil {
		return nil, err
	}
	resp, err := p.send(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	type partialCall struct {
		id, name string
		args     strings.Builder
	}
	var (
		content strings.Builder
		calls   []*partialCall
		usage   *schema.Usage
	)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // 單一片段可能帶很長的工具參數，預設 64KB 上限不夠
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue // SSE 的空行、註解、event: 等
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk oaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("解析串流片段失敗: %w\n%s", err, truncate(data, 300))
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("OpenAI 相容 API 串流錯誤: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			usage = toUsage(*chunk.Usage)
		}
		for _, ch := range chunk.Choices {
			if d := ch.Delta.Content; d != "" {
				content.WriteString(d)
				if onDelta != nil {
					onDelta(d)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				if tc.Index < 0 {
					continue
				}
				for len(calls) <= tc.Index {
					calls = append(calls, &partialCall{})
				}
				c := calls[tc.Index]
				if tc.ID != "" {
					c.id = tc.ID
				}
				if tc.Function.Name != "" {
					c.name = tc.Function.Name
				}
				c.args.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("讀取串流失敗: %w", err)
	}

	result := &schema.Message{Role: schema.RoleAssistant, Content: content.String(), Usage: usage}
	for _, c := range calls {
		result.ToolCalls = append(result.ToolCalls, toolCall(c.id, c.name, c.args.String()))
	}
	return result, nil
}

// requestBody 組 chat-completions 請求。Generate 與 GenerateStream 共用，兩條路徑送出的參數才不會漂移。
func (p *OpenAIProvider) requestBody(msgs []schema.Message, availableTools []schema.ToolDefinition, stream bool) ([]byte, error) {
	if p.cfg.APIKey == "" {
		return nil, fmt.Errorf("缺少 OPENAI_API_KEY（OpenAI 相容 provider）")
	}
	req := oaiRequest{Model: p.cfg.Model, Messages: toOpenAIMessages(msgs),
		ReasoningEffort: p.cfg.ReasoningEffort, MaxCompletionTokens: p.cfg.MaxTokens}
	if len(availableTools) > 0 {
		req.Tools = toOpenAITools(availableTools)
	}
	if stream {
		req.Stream, req.StreamOptions = true, &oaiStreamOptions{IncludeUsage: true}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化請求失敗: %w", err)
	}
	return body, nil
}

// send 送出請求並回傳 HTTP 200 的 response（body 由呼叫端讀取、關閉）。網路錯誤、429、5xx 退避重試；
// 其他狀態碼（401/400…）是使用者端錯誤，重試無益，直接解析成錯誤回傳。
func (p *OpenAIProvider) send(ctx context.Context, body []byte) (*http.Response, error) {
	url := p.cfg.BaseURL + "/chat/completions"
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

		resp, doErr := p.client.Do(req)
		if doErr != nil {
			if attempt < maxRetries && ctx.Err() == nil {
				log.Printf("[OpenAI] 請求失敗（%v），退避後重試 %d/%d", doErr, attempt+1, maxRetries)
				if !sleepBackoff(ctx, attempt, 0) {
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("OpenAI 相容 API 請求失敗: %w", doErr)
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		raw, _ := io.ReadAll(resp.Body)
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		resp.Body.Close()

		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < maxRetries && ctx.Err() == nil {
			log.Printf("[OpenAI] HTTP %d，退避後重試 %d/%d", resp.StatusCode, attempt+1, maxRetries)
			if !sleepBackoff(ctx, attempt, retryAfter) {
				return nil, ctx.Err()
			}
			continue
		}
		var e struct {
			Error *oaiError `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != nil {
			return nil, fmt.Errorf("OpenAI 相容 API 錯誤（HTTP %d）: %s", resp.StatusCode, e.Error.Message)
		}
		return nil, fmt.Errorf("OpenAI 相容 API 錯誤（HTTP %d）: %s", resp.StatusCode, truncate(string(raw), 300))
	}
}

// toolCall 組一個工具呼叫；參數為空時補 {}（模型呼叫無參數工具時常回空字串，下游要合法 JSON）。
func toolCall(id, name, args string) schema.ToolCall {
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	return schema.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

// toUsage 正規化用量；沒有任何 token 數時回 nil（與「端點沒回 usage」同義）。
func toUsage(u oaiUsage) *schema.Usage {
	if u.PromptTokens <= 0 && u.CompletionTokens <= 0 {
		return nil
	}
	cached := u.PromptTokensDetails.CachedTokens
	return &schema.Usage{
		// prompt_tokens 含 cached_tokens；系統約定 PromptTokens 不含快取（見 schema.Usage），不扣會重複計價。
		PromptTokens:     u.PromptTokens - cached,
		CompletionTokens: u.CompletionTokens,
		CacheReadTokens:  cached,
	}
}

// sleepBackoff 在重試前退避等待：優先用伺服器的 Retry-After，否則指數退避（0.5s、1s、2s…），封頂
// maxBackoff。等待期間尊重 ctx 取消——回傳 false 表示 ctx 已取消，呼叫端應中止。
func sleepBackoff(ctx context.Context, attempt int, retryAfter time.Duration) bool {
	d := retryAfter
	if d <= 0 {
		d = time.Duration(500*(1<<attempt)) * time.Millisecond
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// parseRetryAfter 解析 Retry-After 標頭的秒數形式（OpenAI 與多數相容端點用此形式）；非秒數（HTTP 日期）
// 或缺失則回 0，交由呼叫端退回指數退避。
func parseRetryAfter(v string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// truncate 截 API 回應給錯誤訊息用。按字元切——回應可能含中文，byte 切會產生非法 UTF-8。
func truncate(s string, max int) string {
	return schema.TruncRunes(s, max, "…")
}
