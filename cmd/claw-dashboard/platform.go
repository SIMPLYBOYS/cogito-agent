package main

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
)

// platformData 是 /platform 頁的唯讀資料：實際驅動 agent 的平台設定（全 env 驅動，本專案無集中 config
// struct）。祕密（API key / token / secret）一律只顯示「已設定/未設」，絕不露值——即使綁 loopback。
// provider 解析對照 internal/provider.FromEnv（此處唯讀鏡像，故設定即使不完整也看得到，不像 FromEnv 會報錯）。
type platformData struct {
	// LLM（可就地編輯 provider/模型；金鑰仍只看狀態）
	Provider    string // 解析後：Claude / OpenAI 相容 / 未知（目前生效）
	ProviderRaw string // COGITO_PROVIDER 原值（供編輯表單預填 + resolveProviderInto）
	Model       string // 解析後生效模型
	ClaudeModel string // CLAUDE_MODEL 原值（編輯用）
	OpenAIModel string // OPENAI_MODEL 原值
	OpenAIBase  string // OPENAI_BASE_URL 原值
	KeyName     string
	KeySet      bool
	ProviderErr string // 解析不出（未設 key / 未知 provider）時的提示
	Embedder    string // 模型 id，或「關鍵字退回（未設）」
	// 通道 / 可觀測性 / 執行環境（檢視）
	Channels     []channelRow
	Langfuse     bool
	OTelEndpoint string
	Sandbox      string
	MCPConfig    string
	SessionDir   string
	MaxTurns     int
	MaxCostUSD   float64
	MaxConcurrent int
	// 可調護欄（.claw/config.json，熱載）
	Knobs    evolve.Knobs
	Limits   evolve.KnobLimits
	KnobsSet bool
	// 非祕密 env 設定，分主題（各一個就地編輯表單；寫 .env，重啟套用）
	AccessEnv  []envField
	RuntimeEnv []envField
	EvolveEnv  []envField
	ObsEnv     []envField
	Flash      string
	// MCP servers（.mcp.json，列/增/刪/切換/編輯；env/headers 值遮罩）
	MCPServers []mcpServerRow
	MCPPath    string
}

type channelRow struct{ Name, Status string }

func (s *server) platform(w http.ResponseWriter, r *http.Request) {
	d := platformData{
		ProviderRaw:   os.Getenv("COGITO_PROVIDER"),
		ClaudeModel:   os.Getenv("CLAUDE_MODEL"),
		OpenAIModel:   os.Getenv("OPENAI_MODEL"),
		OpenAIBase:    os.Getenv("OPENAI_BASE_URL"),
		Embedder:      envOr("COGITO_EMBED_MODEL", "關鍵字退回（未設 embedder）"),
		Langfuse:      envSet("LANGFUSE_PUBLIC_KEY") && envSet("LANGFUSE_SECRET_KEY"),
		OTelEndpoint:  firstNonEmpty(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
		Sandbox:       onOff("COGITO_SANDBOX"),
		MCPConfig:     envOr("COGITO_MCP_CONFIG", "（未設）"),
		SessionDir:    envOr("COGITO_SESSION_DIR", "（未設）"),
		MaxTurns:      engine.DefaultMaxTurns,
		MaxCostUSD:    engine.DefaultMaxCostUSD,
		MaxConcurrent: engine.DefaultMaxConcurrentTools,
	}
	resolveProviderInto(&d)
	d.Channels = []channelRow{
		{"Slack", boundStatus(envSet("SLACK_BOT_TOKEN") && envSet("SLACK_APP_TOKEN"))},
		{"Telegram", boundStatus(envSet("TELEGRAM_BOT_TOKEN"))},
	}
	d.Limits = evolve.Limits()
	d.Knobs, d.KnobsSet = evolve.LoadKnobs(s.workspace) // 已套用的執行時覆蓋（.claw/config.json）
	d.AccessEnv = loadEnvGroup(accessEnv)
	d.RuntimeEnv = loadEnvGroup(runtimeEnv)
	d.EvolveEnv = loadEnvGroup(evolveEnv)
	d.ObsEnv = loadEnvGroup(obsEnv)
	d.Flash = s.readFlash()
	d.MCPPath = mcpConfigPath()
	d.MCPServers, _ = readMCPServers(d.MCPPath)

	var b bytes.Buffer
	_ = platformTmpl.Execute(&b, d)
	s.render(w, "Platform", template.HTML(b.String()))
}

// resolveProviderInto 鏡像 provider.FromEnv 的選擇邏輯（唯讀，不建構 provider、不因缺 key 報錯）。
func resolveProviderInto(d *platformData) {
	switch strings.ToLower(strings.TrimSpace(d.ProviderRaw)) {
	case "openai", "openai-compatible", "oai":
		d.Provider = "OpenAI 相容"
		d.Model = envOr("OPENAI_MODEL", "gpt-4o-mini")
		d.KeyName, d.KeySet = "OPENAI_API_KEY", envSet("OPENAI_API_KEY")
		if !d.KeySet {
			d.ProviderErr = "COGITO_PROVIDER=openai 但未設 OPENAI_API_KEY，bot 會啟動失敗"
		}
	case "", "claude", "anthropic":
		d.Provider = "Claude"
		d.Model = envOr("CLAUDE_MODEL", "claude-opus-4-8")
		d.KeyName, d.KeySet = "ANTHROPIC_API_KEY", envSet("ANTHROPIC_API_KEY")
		if !d.KeySet {
			d.ProviderErr = "未設 ANTHROPIC_API_KEY，bot 會啟動失敗"
		}
	default:
		d.Provider = "未知"
		d.ProviderErr = fmt.Sprintf("未知的 COGITO_PROVIDER=%q（支援 claude / openai）", d.ProviderRaw)
	}
}

func envSet(key string) bool { return strings.TrimSpace(os.Getenv(key)) != "" }

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func onOff(key string) string {
	if envSet(key) {
		return "已啟用"
	}
	return "停用"
}

func boundStatus(ok bool) string {
	if ok {
		return "已綁定"
	}
	return "未綁定"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var platformTmpl = template.Must(template.New("platform").Parse(`
<p class="muted">祕密遮罩唯讀，其餘可就地編輯。</p>
{{with .Flash}}<div class="banner done">{{.}}</div>{{end}}

<h2>Provider <span class="muted">寫 .env · 改完重啟</span></h2>
{{if .ProviderErr}}<p class="warn">⚠️ {{.ProviderErr}}</p>{{end}}
<form method="POST" action="/env-config" class="knobs">
  <input type="hidden" name="_fields" value="COGITO_PROVIDER CLAUDE_MODEL OPENAI_MODEL OPENAI_BASE_URL">
  <label>provider <span class="muted">claude / openai（空＝claude）</span><input type="text" name="COGITO_PROVIDER" value="{{.ProviderRaw}}"></label>
  <label>Claude 模型 <span class="muted">空＝預設</span><input type="text" name="CLAUDE_MODEL" value="{{.ClaudeModel}}"></label>
  <label>OpenAI 模型 <span class="muted">provider=openai 時</span><input type="text" name="OPENAI_MODEL" value="{{.OpenAIModel}}"></label>
  <label>OpenAI base URL <span class="muted">vLLM/Ollama/OpenRouter…</span><input type="text" name="OPENAI_BASE_URL" value="{{.OpenAIBase}}"></label>
  <button type="submit">儲存</button>
</form>
<dl class="kv">
  <dt>目前生效</dt><dd><span class="badge">{{.Provider}}</span> · <code>{{.Model}}</code></dd>
  <dt>{{.KeyName}}</dt><dd>{{if .KeySet}}已設定 ✓{{else}}<span class="warn">未設 —</span>（金鑰請手動編 .env）{{end}}</dd>
  <dt>embedder</dt><dd><code>{{.Embedder}}</code></dd>
</dl>

<h2>通道 <span class="muted">檢視 · token 在 .env 設</span></h2>
<dl class="kv">
  {{range .Channels}}<dt>{{.Name}}</dt><dd>{{if eq .Status "已綁定"}}<span class="badge">已綁定</span>{{else}}<span class="muted">未綁定</span>{{end}}</dd>{{end}}
</dl>

<h2>存取控制 <span class="muted">寫 .env · 改完重啟</span></h2>
{{template "envform" .AccessEnv}}

<h2>執行環境 <span class="muted">寫 .env · 改完重啟</span></h2>
{{template "envform" .RuntimeEnv}}
<p class="muted">回合／成本／併發上限見下方「護欄」（熱載免重啟）。</p>

<h2>可觀測性 <span class="muted">Langfuse 檢視 · OTel 可編輯</span></h2>
<dl class="kv">
  <dt>Langfuse</dt><dd>{{if .Langfuse}}<span class="badge">已接</span>{{else}}<span class="muted">未接</span>{{end}} <span class="muted">（金鑰在 .env）</span></dd>
</dl>
{{template "envform" .ObsEnv}}

<h2>自我進化 <span class="muted">寫 .env · 改完重啟</span></h2>
{{template "envform" .EvolveEnv}}

<h2>MCP 伺服器 <span class="muted">改完重啟</span></h2>
{{if .MCPServers}}<div class="mcplist">{{range .MCPServers}}<div class="mcpitem">
  <div class="mcprow">
    <span class="mcpinfo"><b>{{.Name}}</b> <span class="badge">{{.Type}}</span>{{if .Disabled}} <span class="muted">停用</span>{{end}}{{if .HasSecrets}} <span class="muted">🔒 有祕密</span>{{end}}<br><span class="muted">{{.Target}}</span></span>
    <span class="acts">
      <form method="POST" action="/mcp/toggle"><input type="hidden" name="name" value="{{.Name}}"><button type="submit" class="gact ghost">{{if .Disabled}}啟用{{else}}停用{{end}}</button></form>
      <form method="POST" action="/mcp/remove"><input type="hidden" name="name" value="{{.Name}}"><button type="submit" class="gact ghost">移除</button></form>
    </span>
  </div>
  <details class="mcpedit"><summary>編輯</summary>
    <form method="POST" action="/mcp/edit" class="knobs">
      <input type="hidden" name="name" value="{{.Name}}">
      <label>類型<select name="type"><option value="stdio"{{if ne .Type "http"}} selected{{end}}>stdio</option><option value="http"{{if eq .Type "http"}} selected{{end}}>http</option></select></label>
      <label>{{if eq .Type "http"}}url{{else}}command{{end}}<input type="text" name="target" value="{{if eq .Type "http"}}{{.URL}}{{else}}{{.Command}}{{end}}"></label>
      <label>args<input type="text" name="args" value="{{.ArgsStr}}"></label>
      {{if or .EnvKeys .HeaderKeys}}<p class="hint">🔒 保留不動（改值編 .mcp.json）：{{range .EnvKeys}}<code>env:{{.}}</code> {{end}}{{range .HeaderKeys}}<code>header:{{.}}</code> {{end}}</p>{{end}}
      <button type="submit">儲存</button>
    </form>
  </details>
</div>{{end}}</div>{{else}}<p class="muted">尚無 server。</p>{{end}}
<details class="mcpedit"><summary>＋ 新增 server</summary>
  <form method="POST" action="/mcp/add" class="knobs">
    <label>名稱<input type="text" name="name" placeholder="twinkle-hub"></label>
    <label>類型<select name="type"><option value="stdio">stdio</option><option value="http">http</option></select></label>
    <label>command 或 url<input type="text" name="target" placeholder="npx… 或 https://…"></label>
    <label>args<input type="text" name="args" placeholder="-y @scope/pkg"></label>
    <button type="submit">新增</button>
  </form>
</details>

<h2>護欄 <span class="muted">熱載 · 免重啟</span></h2>
<p class="muted">0＝用預設；送出自動 clamp。{{if .KnobsSet}}目前有覆蓋。{{else}}目前用預設。{{end}}</p>
<form method="POST" action="/config" class="knobs">
  <label>回合上限 <span class="muted">{{.Limits.MinTurns}}–{{.Limits.MaxTurns}}</span>
    <input type="number" name="max_turns" value="{{.Knobs.MaxTurns}}" min="0" max="{{.Limits.MaxTurns}}"></label>
  <label>工具併發 <span class="muted">{{.Limits.MinConcurrency}}–{{.Limits.MaxConcurrency}}</span>
    <input type="number" name="max_concurrent" value="{{.Knobs.MaxConcurrentTools}}" min="0" max="{{.Limits.MaxConcurrency}}"></label>
  <label>成本熔斷 $ <span class="muted">{{printf "%.1f" .Limits.MinCostUSD}}–{{printf "%.1f" .Limits.MaxCostUSD}}</span>
    <input type="number" step="0.01" name="max_cost" value="{{printf "%.2f" .Knobs.MaxCostUSD}}" min="0" max="{{.Limits.MaxCostUSD}}"></label>
  <button type="submit">儲存</button>
</form>

{{define "envform"}}<form method="POST" action="/env-config" class="knobs">
  <input type="hidden" name="_fields" value="{{range .}}{{.Key}} {{end}}">
  {{range .}}<label>{{.Label}}{{with .Hint}} <span class="muted">{{.}}</span>{{end}}
    {{if .Toggle}}<span class="tog"><input type="checkbox" name="{{.Key}}" value="1"{{if .On}} checked{{end}}> 啟用</span>{{else}}<input type="text" name="{{.Key}}" value="{{.Value}}" placeholder="{{.Hint}}">{{end}}</label>
  {{end}}<button type="submit">儲存</button>
</form>{{end}}
`))
