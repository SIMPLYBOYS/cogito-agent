package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func TestOpenAIProvider_MapsRolesToolsAndParsesResponse(t *testing.T) {
	var gotReq oaiRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"好","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"x\"}"}}]}}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":3}}}`)
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "test-key", Model: "gpt-4o-mini", HTTPClient: srv.Client()})

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "做事"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "call_0", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)}}},
		{Role: schema.RoleUser, ToolCallID: "call_0", Content: "檔案列表"},
	}
	tools := []schema.ToolDefinition{{Name: "read_file", Description: "讀檔", InputSchema: map[string]any{"type": "object"}}}

	resp, err := p.Generate(context.Background(), msgs, tools)
	if err != nil {
		t.Fatalf("Generate 失敗: %v", err)
	}

	// --- 驗證送出的請求映射 ---
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization 應為 Bearer test-key，got %q", gotAuth)
	}
	if gotReq.Model != "gpt-4o-mini" || len(gotReq.Messages) != 4 {
		t.Fatalf("請求映射錯誤: model=%q msgs=%d", gotReq.Model, len(gotReq.Messages))
	}
	if gotReq.Messages[0].Role != "system" || gotReq.Messages[1].Role != "user" {
		t.Errorf("system/user 映射錯誤: %+v", gotReq.Messages[:2])
	}
	if gotReq.Messages[2].Role != "assistant" || len(gotReq.Messages[2].ToolCalls) != 1 ||
		gotReq.Messages[2].ToolCalls[0].Function.Arguments != `{"command":"ls"}` {
		t.Errorf("assistant tool_calls 映射錯誤: %+v", gotReq.Messages[2])
	}
	if gotReq.Messages[3].Role != "tool" || gotReq.Messages[3].ToolCallID != "call_0" {
		t.Errorf("tool 結果（role:tool + tool_call_id）映射錯誤: %+v", gotReq.Messages[3])
	}
	if len(gotReq.Tools) != 1 || gotReq.Tools[0].Function.Name != "read_file" {
		t.Errorf("tools 映射錯誤: %+v", gotReq.Tools)
	}

	// --- 驗證回應解析 ---
	if resp.Content != "好" || len(resp.ToolCalls) != 1 {
		t.Fatalf("回應解析錯誤: %+v", resp)
	}
	if resp.ToolCalls[0].Name != "read_file" || string(resp.ToolCalls[0].Arguments) != `{"path":"x"}` {
		t.Errorf("tool_call 解析錯誤: %+v", resp.ToolCalls[0])
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 10 || resp.Usage.CacheReadTokens != 3 {
		t.Errorf("usage 解析錯誤: %+v", resp.Usage)
	}
}

func TestOpenAIProvider_MissingKey(t *testing.T) {
	p := NewOpenAIProvider(OpenAIConfig{Model: "x"})
	if _, err := p.Generate(context.Background(), nil, nil); err == nil {
		t.Error("缺 API key 應回 error")
	}
}

func TestOpenAIProvider_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid key"}}`)
	}))
	defer srv.Close()
	p := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "bad", Model: "x", HTTPClient: srv.Client()})
	_, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("HTTP 401 應回 error")
	}
}

func TestOpenAIProvider_Defaults(t *testing.T) {
	p := NewOpenAIProvider(OpenAIConfig{APIKey: "k"})
	if p.ModelName() != "gpt-4o-mini" || p.MaxContextTokens() != 128000 {
		t.Errorf("預設值錯誤: model=%s ctx=%d", p.ModelName(), p.MaxContextTokens())
	}
}
