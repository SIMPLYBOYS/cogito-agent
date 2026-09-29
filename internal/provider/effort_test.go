package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// capabilities.effort 的原始 JSON → 收哪幾級，照 low→max 排；xhigh 在 SDK 的欄位外也要認得。
func TestEffortLevels(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{`{"supported":true,"max":{"supported":true},"low":{"supported":true},"xhigh":{"supported":true},"medium":{"supported":false},"high":{"supported":true}}`,
			[]string{"low", "high", "xhigh", "max"}},
		{`{"supported":false,"low":{"supported":false}}`, []string{}},
		{"", nil},
	}
	for _, c := range cases {
		if got := effortLevels(c.raw); !reflect.DeepEqual(got, c.want) {
			t.Errorf("effortLevels(%s) = %#v，want %#v", c.raw, got, c.want)
		}
	}
}

// Claude：型號收才送 output_config.effort——Haiku 收到 effort 會 400。清單還沒到手時，已知不收的也不送。
func TestClaudeProvider_EffortOnlyWhenModelSupports(t *testing.T) {
	windowsMu.Lock()
	saved := efforts
	efforts = map[string][]string{"claude-opus-5": {"low", "medium", "high", "xhigh", "max"}, "claude-haiku-4-5": {}}
	windowsMu.Unlock()
	defer func() { windowsMu.Lock(); efforts = saved; windowsMu.Unlock() }()

	msgs := []schema.Message{{Role: schema.RoleUser, Content: "hi"}}
	sent := func(model, effort string) string {
		p := (&ClaudeProvider{model: model}).WithEffort(effort).(*ClaudeProvider)
		body, _ := json.Marshal(p.buildParams(msgs, nil))
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		oc, _ := m["output_config"].(map[string]any)
		e, _ := oc["effort"].(string)
		return e
	}
	for _, c := range []struct{ model, effort, want string }{
		{"claude-opus-5", "xhigh", "xhigh"},
		{"claude-opus-5", "ultra", ""},      // 這個型號沒這一級
		{"claude-haiku-4-5", "high", ""},    // 明確不收
		{"claude-haiku-4-6", "high", ""},    // 清單裡沒有：後備判斷認得 Haiku
		{"claude-sonnet-9", "high", "high"}, // 清單裡沒有、也不在已知不收的：照送
		{"claude-opus-5", "", ""},           // 沒選＝不送
	} {
		if got := sent(c.model, c.effort); got != c.want {
			t.Errorf("%s effort=%q：送了 %q，want %q", c.model, c.effort, got, c.want)
		}
	}
	// Configure（子 agent 換模型）是值拷貝：頻道的 effort 要跟著走
	p := (&ClaudeProvider{model: "claude-opus-5"}).WithEffort("high").(*ClaudeProvider).Configure("", 8192).(*ClaudeProvider)
	if p.effort != "high" {
		t.Errorf("Configure 後 effort 掉了：%q", p.effort)
	}
}

// OpenAI 相容：頻道的 effort 蓋過 OPENAI_REASONING_EFFORT（那是整個行程的預設）。
func TestOpenAIProvider_WithEffortOverridesEnv(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_REASONING_EFFORT", "low")
	p := NewOpenAIProvider(openAIConfigFromEnv()).WithEffort("high")
	if _, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" {
		t.Errorf("頻道 effort 要蓋過環境預設，送了 %v", body["reasoning_effort"])
	}
}
