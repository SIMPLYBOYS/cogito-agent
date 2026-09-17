package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	// OpenAI 的 prompt_tokens【含】快取命中的 cached_tokens；系統約定 PromptTokens 不含快取
	// （計價另以 0.1x 算 CacheReadTokens、面板以 PromptTokens+CacheRead 算總輸入），不扣就重複計價。
	if resp.Usage == nil || resp.Usage.PromptTokens != 7 || resp.Usage.CacheReadTokens != 3 || resp.Usage.InputTokens() != 10 {
		t.Errorf("usage 應正規化成 PromptTokens=7（10−3 快取）、CacheRead=3、總輸入 10: %+v", resp.Usage)
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

func TestParseRetryAfter(t *testing.T) {
	if parseRetryAfter("2") != 2*time.Second {
		t.Error("秒數形式應解析為對應秒數")
	}
	if parseRetryAfter("") != 0 || parseRetryAfter("Wed, 21 Oct 2015 07:28:00 GMT") != 0 {
		t.Error("缺失或 HTTP 日期形式應回 0（退回指數退避）")
	}
}

func TestSleepBackoff_RespectsCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepBackoff(ctx, 5, 0) {
		t.Error("ctx 已取消應立即回 false")
	}
	if !sleepBackoff(context.Background(), 0, time.Millisecond) {
		t.Error("正常等待應回 true")
	}
}

// 429 連兩次後成功：驗證會重試而非整個任務中止（P5 的核心韌性缺口）。
func TestOpenAIProvider_RetriesOn429ThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "x", HTTPClient: srv.Client()})
	resp, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("429 兩次後應重試成功: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("內容錯誤: %q", resp.Content)
	}
	if n := atomic.LoadInt32(&hits); n != 3 {
		t.Errorf("應重試到第 3 次才成功，實際打了 %d 次", n)
	}
}

// 4xx（非 429）不重試：使用者端錯誤重試無益，應立即失敗、只打一次。
func TestOpenAIProvider_NoRetryOn4xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"bad request"}}`)
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "x", HTTPClient: srv.Client()})
	if _, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hi"}}, nil); err == nil {
		t.Fatal("HTTP 400 應回 error")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("4xx 不該重試，應只打 1 次，實際 %d 次", n)
	}
}

// okServer 是只回一句話的 OpenAI 相容假端點，記下收到的 model 與 Authorization。
func okServer(t *testing.T, gotModel, gotAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req oaiRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		*gotModel, *gotAuth = req.Model, r.Header.Get("Authorization")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 主引擎跑 Claude 時，頻道 `model gpt-…`、具名 agent 的 model、COGITO_REFLECT_MODEL 指到非 Claude 模型，
// 請求要送到 OpenAI 相容端點——而不是把 gpt id 丟給 Anthropic，換一句「找不到模型」。
func TestClaudeConfigure_NonClaudeModelGoesToOpenAIEndpoint(t *testing.T) {
	var gotModel, gotAuth string
	srv := okServer(t, &gotModel, &gotAuth)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "oai-key")
	t.Setenv("OPENAI_MAX_CONTEXT_TOKENS", "64000")

	p := (&ClaudeProvider{model: "claude-opus-5"}).Configure("gpt-4o-mini", 0)
	if _, ok := p.(*OpenAIProvider); !ok {
		t.Fatalf("指定 gpt-4o-mini 仍留在 %T，請求會送去 Anthropic", p)
	}
	if _, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("Generate 失敗: %v", err)
	}
	if gotModel != "gpt-4o-mini" || gotAuth != "Bearer oai-key" {
		t.Errorf("端點收到 model=%q auth=%q，want gpt-4o-mini / Bearer oai-key", gotModel, gotAuth)
	}
	if w := p.MaxContextTokens(); w != 64000 {
		t.Errorf("窗口應取 OPENAI_MAX_CONTEXT_TOKENS=64000，got %d", w)
	}
	if c := (&ClaudeProvider{model: "claude-opus-5"}).Configure("claude-haiku-4-5", 0); c.ModelName() != "claude-haiku-4-5" {
		t.Errorf("claude- 模型應留在 Claude，got %T %q", c, c.ModelName())
	}
}

// 內建的審查類具名 agent 都寫 model: claude-opus-4-8。主引擎是 OpenAI 相容端點時：沒有 Anthropic 金鑰就
// 沿用本端點的模型（把 claude id 送去別家只會換來一次必然失敗的呼叫）；有金鑰就改走 Claude。
func TestOpenAIConfigure_ClaudeModel(t *testing.T) {
	var gotModel, gotAuth string
	srv := okServer(t, &gotModel, &gotAuth)
	base := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "gpt-4o-mini", HTTPClient: srv.Client()})

	t.Setenv("ANTHROPIC_API_KEY", "")
	p := base.Configure("claude-opus-4-8", 0)
	if _, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("Generate 失敗: %v", err)
	}
	if gotModel != "gpt-4o-mini" {
		t.Errorf("沒有 Anthropic 金鑰時應沿用 gpt-4o-mini，端點卻收到 %q", gotModel)
	}

	t.Setenv("ANTHROPIC_API_KEY", "ant-key")
	seedWindows(t, map[string]int{}) // 讓 NewClaudeProvider 不在背景打網路
	cp, ok := base.Configure("claude-opus-4-8", 2048).(*ClaudeProvider)
	if !ok || cp.model != "claude-opus-4-8" || cp.maxTokens != 2048 {
		t.Fatalf("有 Anthropic 金鑰時應改走 Claude（claude-opus-4-8, maxTokens 2048），got %+v ok=%v", cp, ok)
	}
}

// gpt-5.6-sol 在 /v1/chat/completions 上「工具＋推理」不能同時開（HTTP 400：Function tools with
// reasoning_effort are not supported）。OPENAI_REASONING_EFFORT 有設才送 reasoning_effort；沒設就
// 【不送】——gpt-4o-mini 這類非推理模型與多數本地端點收到這個欄位會拒收。
func TestOpenAIProvider_ReasoningEffortFromEnv(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	msgs := []schema.Message{{Role: schema.RoleUser, Content: "hi"}}
	tools := []schema.ToolDefinition{{Name: "bash", InputSchema: map[string]any{"type": "object"}}}

	t.Setenv("OPENAI_REASONING_EFFORT", "none")
	p := NewOpenAIProvider(openAIConfigFromEnv())
	for name, prov := range map[string]LLMProvider{"主引擎": p, "Configure 換模型後": p.Configure("gpt-5.6-sol", 0)} {
		if _, err := prov.Generate(context.Background(), msgs, tools); err != nil {
			t.Fatalf("%s Generate 失敗: %v", name, err)
		}
		if body["reasoning_effort"] != "none" {
			t.Errorf("%s：設了 OPENAI_REASONING_EFFORT=none，請求卻帶 reasoning_effort=%v", name, body["reasoning_effort"])
		}
	}

	t.Setenv("OPENAI_REASONING_EFFORT", "")
	if _, err := NewOpenAIProvider(openAIConfigFromEnv()).Generate(context.Background(), msgs, tools); err != nil {
		t.Fatalf("Generate 失敗: %v", err)
	}
	if v, has := body["reasoning_effort"]; has {
		t.Errorf("沒設 OPENAI_REASONING_EFFORT 時不該送 reasoning_effort，實際送了 %v", v)
	}
}

// 具名 agent 的 effort 經 Configure 傳入輸出上限；OpenAI 相容路徑先前靜默丟掉。有值才送
// max_completion_tokens（推理模型已不收 max_tokens），沒值不送、由端點決定。
func TestOpenAIProvider_EffortSendsMaxCompletionTokens(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	msgs := []schema.Message{{Role: schema.RoleUser, Content: "hi"}}
	base := NewOpenAIProvider(openAIConfigFromEnv())

	cases := []struct {
		name string
		p    LLMProvider
		want any
	}{
		{"主引擎未設上限", base, nil},
		{"effort=high（8192）", base.Configure("", 8192), float64(8192)},
		{"Claude 主引擎路由到 OpenAI 模型", (&ClaudeProvider{model: "claude-opus-5"}).Configure("gpt-5.6-sol", 4096), float64(4096)},
	}
	for _, c := range cases {
		if _, err := c.p.Generate(context.Background(), msgs, nil); err != nil {
			t.Fatalf("%s: Generate 失敗: %v", c.name, err)
		}
		if got := body["max_completion_tokens"]; got != c.want {
			t.Errorf("%s: max_completion_tokens = %v，want %v", c.name, got, c.want)
		}
	}
}

// 面板內嵌 chat 靠 StreamingProvider 逐字顯示；OpenAI 相容路徑先前沒有實作，只能等整段回完。
// 串流要做到三件事：文字增量即時吐給 onDelta、分散在多個片段的 tool call 參數拼回完整 JSON、
// 最後的 usage 片段（stream_options.include_usage）照一次性 Generate 的語意正規化。
func TestOpenAIProvider_GenerateStream(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"choices":[{"delta":{"role":"assistant","content":"你"}}]}`,
			`{"choices":[{"delta":{"content":"好"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`,
		} {
			io.WriteString(w, "data: "+chunk+"\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var p LLMProvider = NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "gpt-5.6-sol", HTTPClient: srv.Client()})
	sp, ok := p.(StreamingProvider)
	if !ok {
		t.Fatal("OpenAIProvider 沒實作 StreamingProvider：面板 chat 無法逐字串流")
	}
	var deltas []string
	msg, err := sp.GenerateStream(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hi"}},
		[]schema.ToolDefinition{{Name: "read_file", InputSchema: map[string]any{"type": "object"}}},
		func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("GenerateStream 失敗: %v", err)
	}
	if body["stream"] != true {
		t.Errorf("請求應帶 stream:true，got %v", body["stream"])
	}
	if so, _ := body["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Errorf("請求應帶 stream_options.include_usage:true（否則拿不到 usage、成本與校準全失效），got %v", body["stream_options"])
	}
	if strings.Join(deltas, "") != "你好" || len(deltas) != 2 || msg.Content != "你好" {
		t.Errorf("文字增量應逐片吐出並組回完整內容：deltas=%q content=%q", deltas, msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].Name != "read_file" ||
		string(msg.ToolCalls[0].Arguments) != `{"path":"a.go"}` {
		t.Errorf("分片的 tool call 應拼回完整參數: %+v", msg.ToolCalls)
	}
	if msg.Usage == nil || msg.Usage.PromptTokens != 6 || msg.Usage.CacheReadTokens != 4 || msg.Usage.CompletionTokens != 5 {
		t.Errorf("usage 應正規化成 PromptTokens=6（10−4 快取）、CacheRead=4、Completion=5: %+v", msg.Usage)
	}
}

// 串流請求被拒（例如 400）時要回錯誤、帶出 API 的錯誤訊息，而不是回一則空訊息假裝成功。
func TestOpenAIProvider_GenerateStreamAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"Function tools with reasoning_effort are not supported"}}`)
	}))
	defer srv.Close()
	var p LLMProvider = NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "x", HTTPClient: srv.Client()})
	sp, ok := p.(StreamingProvider)
	if !ok {
		t.Fatal("OpenAIProvider 沒實作 StreamingProvider")
	}
	_, err := sp.GenerateStream(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hi"}}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "reasoning_effort are not supported") {
		t.Fatalf("400 應回錯誤並帶出 API 訊息，got %v", err)
	}
}

// office 的人設模型選單：provider 不實作 ModelLister 時退回內建計價表，而那張表只有 Claude——
// 只走 OpenAI 的部署會在選單裡看到一排選了也不會生效的 claude 模型。/v1/models 混著 embedding、
// 語音、繪圖等非對話模型，選它們當人設只會失敗，要濾掉。
func TestOpenAIProvider_ListModels(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		io.WriteString(w, `{"object":"list","data":[
			{"id":"gpt-5.6-terra","object":"model"},{"id":"text-embedding-3-small","object":"model"},
			{"id":"whisper-1","object":"model"},{"id":"gpt-5.6-sol","object":"model"},
			{"id":"tts-1","object":"model"},{"id":"dall-e-3","object":"model"},
			{"id":"omni-moderation-latest","object":"model"},{"id":"gpt-5.6-luna","object":"model"}]}`)
	}))
	defer srv.Close()

	var p LLMProvider = NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()})
	l, ok := p.(ModelLister)
	if !ok {
		t.Fatal("OpenAIProvider 沒實作 ModelLister：office 選單只會列出內建計價表裡的 claude 模型")
	}
	got, err := l.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels 失敗: %v", err)
	}
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if want := "gpt-5.6-luna,gpt-5.6-sol,gpt-5.6-terra"; strings.Join(ids, ",") != want {
		t.Errorf("應只留對話模型並排序：got %v want %s", ids, want)
	}
	if gotPath != "/models" || gotAuth != "Bearer k" {
		t.Errorf("應以金鑰打 GET {base}/models，got path=%q auth=%q", gotPath, gotAuth)
	}
}

// bench 跑分、A/B、SWE-bench 生成、ingest -llm 以「模型」為參數，先前一律 NewClaudeProvider：
// 只有 OpenAI 金鑰時不能用，而且缺 Anthropic 金鑰會直接 panic。ForModel 依模型 id 選 provider，
// 缺金鑰回一句看得懂的錯誤。
func TestForModel(t *testing.T) {
	var gotModel, gotAuth string
	srv := okServer(t, &gotModel, &gotAuth)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "oai-key")

	if _, err := ForModel("claude-haiku-4-5"); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("claude 模型缺 Anthropic 金鑰應回錯誤（而不是 panic），got %v", err)
	}

	p, err := ForModel("gpt-5.6-luna")
	if err != nil {
		t.Fatalf("gpt 模型有 OpenAI 金鑰應建得出 provider: %v", err)
	}
	if _, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("Generate 失敗: %v", err)
	}
	if gotModel != "gpt-5.6-luna" || gotAuth != "Bearer oai-key" {
		t.Errorf("請求應送到 OpenAI 相容端點：model=%q auth=%q", gotModel, gotAuth)
	}

	t.Setenv("OPENAI_API_KEY", "")
	if _, err := ForModel("gpt-5.6-luna"); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("gpt 模型缺 OpenAI 金鑰應回錯誤，got %v", err)
	}
}

// 退避要有隨機擾動：同時被限流的請求若算出一模一樣的等待時間，會在同一瞬間一起重試、再一起被限流。
// 伺服器給了 Retry-After 時只往後擾動，絕不早於伺服器要求的時間。
func TestBackoffDelay_Jitter(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 200 {
		d := backoffDelay(2, 0) // 指數基準 2s
		if d < time.Second || d > 2*time.Second {
			t.Fatalf("attempt 2 的退避應落在 [1s, 2s]，got %v", d)
		}
		seen[d] = true

		r := backoffDelay(0, 4*time.Second)
		if r < 4*time.Second || r > 6*time.Second {
			t.Fatalf("Retry-After=4s 的退避應落在 [4s, 6s]（不早於伺服器要求），got %v", r)
		}
		if c := backoffDelay(20, 0); c > maxBackoff {
			t.Fatalf("退避不得超過上限 %v，got %v", maxBackoff, c)
		}
	}
	if len(seen) < 10 {
		t.Errorf("200 次退避只出現 %d 種值：沒有隨機擾動，並發失敗的請求會同步重試", len(seen))
	}
}
