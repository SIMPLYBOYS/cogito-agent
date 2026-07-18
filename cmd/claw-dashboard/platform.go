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
	// LLM
	Provider    string // Claude / OpenAI 相容 / 未知
	ProviderRaw string // COGITO_PROVIDER 原值（空＝預設 Claude）
	Model       string
	ContextWin  string // 僅 OpenAI 有可調窗口
	BaseURL     string // 僅 OpenAI 相容
	KeyName     string
	KeySet      bool
	ProviderErr string // 解析不出（未設 key / 未知 provider）時的提示
	// Embedder（長期記憶 recall 用）
	Embedder    string // 模型 id，或「關鍵字退回（未設）」
	EmbedKeySet bool
	// 通道
	Channels []channelRow
	// 可觀測性
	Langfuse     bool
	OTelEndpoint string
	OTelExporter string
	// 執行環境 + 護欄
	Sandbox        string
	SandboxImage   string
	SandboxNetwork string
	MCPConfig      string
	MCPTimeout     string
	SessionDir     string
	AutoResume     string
	Summary        string
	MaxTurns       int
	MaxCostUSD     float64
	MaxConcurrent  int
	// 自我進化
	MemorySynth string
	SkillSynth  string
	KGSynth     string
	// 可調護欄（.claw/config.json）：執行時覆蓋引擎預設，可就地編輯。
	Knobs    evolve.Knobs
	Limits   evolve.KnobLimits
	KnobsSet bool
	// 可編輯的非祕密 env 設定（寫 .env、重啟套用）；祕密不在其列。
	EnvFields []envField
	Flash     string
	// MCP servers（.mcp.json）：列出 + 增/刪/切換；env/headers 值遮罩。
	MCPServers []mcpServerRow
	MCPPath    string
}

type channelRow struct{ Name, Status string }

func (s *server) platform(w http.ResponseWriter, r *http.Request) {
	d := platformData{
		ProviderRaw:  os.Getenv("COGITO_PROVIDER"),
		Embedder:     envOr("COGITO_EMBED_MODEL", "關鍵字退回（未設 embedder）"),
		EmbedKeySet:  envSet("COGITO_EMBED_API_KEY"),
		Langfuse:     envSet("LANGFUSE_PUBLIC_KEY") && envSet("LANGFUSE_SECRET_KEY"),
		OTelEndpoint: firstNonEmpty(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
		OTelExporter: os.Getenv("OTEL_TRACES_EXPORTER"),
		Sandbox:      onOff("COGITO_SANDBOX"),
		SandboxImage: envOr("COGITO_SANDBOX_IMAGE", "（預設）"),
		SandboxNetwork: envOr("COGITO_SANDBOX_NETWORK", "（預設）"),
		MCPConfig:    envOr("COGITO_MCP_CONFIG", "（未設）"),
		MCPTimeout:   envOr("COGITO_MCP_TIMEOUT", "（預設）"),
		SessionDir:   envOr("COGITO_SESSION_DIR", "（未設）"),
		AutoResume:   onOff("COGITO_AUTO_RESUME"),
		Summary:      onOff("COGITO_SUMMARY"),
		MaxTurns:     engine.DefaultMaxTurns,
		MaxCostUSD:   engine.DefaultMaxCostUSD,
		MaxConcurrent: engine.DefaultMaxConcurrentTools,
		MemorySynth:  onOff("COGITO_MEMORY_SYNTH"),
		SkillSynth:   onOff("COGITO_SKILL_SYNTH"),
		KGSynth:      onOff("COGITO_KG_SYNTH"),
	}
	resolveProviderInto(&d)
	d.Channels = []channelRow{
		{"Slack", boundStatus(envSet("SLACK_BOT_TOKEN") && envSet("SLACK_APP_TOKEN"))},
		{"Telegram", boundStatus(envSet("TELEGRAM_BOT_TOKEN"))},
	}
	d.Limits = evolve.Limits()
	d.Knobs, d.KnobsSet = evolve.LoadKnobs(s.workspace) // 已套用的執行時覆蓋（.claw/config.json）
	d.EnvFields = loadEnvFields()
	d.Flash = s.readFlash()
	d.MCPPath = mcpConfigPath()
	d.MCPServers, _ = readMCPServers(d.MCPPath)

	var b bytes.Buffer
	_ = platformTmpl.Execute(&b, d)
	s.render(w, "Platform（設定檢視）", template.HTML(b.String()))
}

// resolveProviderInto 鏡像 provider.FromEnv 的選擇邏輯（唯讀，不建構 provider、不因缺 key 報錯）。
func resolveProviderInto(d *platformData) {
	switch strings.ToLower(strings.TrimSpace(d.ProviderRaw)) {
	case "openai", "openai-compatible", "oai":
		d.Provider = "OpenAI 相容"
		d.Model = envOr("OPENAI_MODEL", "gpt-4o-mini")
		d.ContextWin = envOr("OPENAI_MAX_CONTEXT_TOKENS", "128000") + " tok"
		d.BaseURL = envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
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
<p class="muted">實際驅動 agent 的平台設定（皆 env 驅動）。祕密只顯示有無、不露值；非祕密設定可在下方就地編輯。</p>
{{with .Flash}}<div class="banner done">{{.}}</div>{{end}}

<h2>LLM Provider</h2>
{{if .ProviderErr}}<p class="warn">⚠️ {{.ProviderErr}}</p>{{end}}
<dl class="kv">
  <dt>provider</dt><dd><span class="badge">{{.Provider}}</span>{{with .ProviderRaw}} <span class="muted">COGITO_PROVIDER={{.}}</span>{{else}} <span class="muted">（未設 → 預設 Claude）</span>{{end}}</dd>
  <dt>模型</dt><dd><code>{{.Model}}</code></dd>
  {{with .ContextWin}}<dt>上下文窗口</dt><dd>{{.}}</dd>{{end}}
  {{with .BaseURL}}<dt>base URL</dt><dd><code>{{.}}</code></dd>{{end}}
  <dt>{{.KeyName}}</dt><dd>{{if .KeySet}}已設定 ✓{{else}}<span class="warn">未設 —</span>{{end}}</dd>
  <dt>embedder</dt><dd><code>{{.Embedder}}</code>{{if .EmbedKeySet}} <span class="muted">（COGITO_EMBED_API_KEY 已設定 ✓）</span>{{end}}</dd>
</dl>

<h2>通道綁定 <span class="muted">bot 端 .env；本頁只檢視</span></h2>
<dl class="kv">
  {{range .Channels}}<dt>{{.Name}}</dt><dd>{{if eq .Status "已綁定"}}<span class="badge">已綁定</span>{{else}}<span class="muted">未綁定</span>{{end}}</dd>{{end}}
</dl>

<h2>可觀測性</h2>
<dl class="kv">
  <dt>Langfuse</dt><dd>{{if .Langfuse}}<span class="badge">已接</span> <span class="muted">（PUBLIC/SECRET key 已設定 ✓）</span>{{else}}<span class="muted">未接</span>{{end}}</dd>
  <dt>OTel traces</dt><dd>{{if .OTelEndpoint}}<code>{{.OTelEndpoint}}</code>{{with .OTelExporter}} <span class="muted">exporter={{.}}</span>{{end}}{{else}}<span class="muted">未設</span>{{end}}</dd>
</dl>

<h2>執行環境與護欄 <span class="muted">框架層強制，不依賴模型自覺</span></h2>
<dl class="kv">
  <dt>sandbox</dt><dd>{{.Sandbox}} <span class="muted">image={{.SandboxImage}} · network={{.SandboxNetwork}}</span></dd>
  <dt>MCP</dt><dd><code>{{.MCPConfig}}</code> <span class="muted">timeout={{.MCPTimeout}}</span></dd>
  <dt>session 目錄</dt><dd><code>{{.SessionDir}}</code></dd>
  <dt>開機自動接續</dt><dd>{{.AutoResume}}</dd>
  <dt>滾動摘要壓縮</dt><dd>{{.Summary}}</dd>
  <dt>單次 Run 回合上限</dt><dd>{{.MaxTurns}} 輪 <span class="muted">（達上限強制中止，防失控重試燒 API）</span></dd>
  <dt>單次 Run 成本熔斷</dt><dd>${{printf "%.2f" .MaxCostUSD}} <span class="muted">（達 80% 先軟著陸提醒交付）</span></dd>
  <dt>單輪工具併發上限</dt><dd>{{.MaxConcurrent}} <span class="muted">（信號量，防瞬間打爆下游）</span></dd>
</dl>

<h2>可編輯設定 <span class="muted">非祕密 · 就地寫 .env · 重啟套用</span></h2>
<p class="warn">⚠️ 寫入 <code>.env</code>（祕密行逐字不動）。env 在 bot 啟動時讀，故【重啟】才套用；金鑰／token 不在此編輯（見上方遮罩檢視）。</p>
<form method="POST" action="/env-config" class="knobs">
  {{range .EnvFields}}<label>{{.Label}}{{with .Hint}} <span class="muted">{{.}}</span>{{end}}
    {{if .Toggle}}<span class="tog"><input type="checkbox" name="{{.Key}}" value="1"{{if .On}} checked{{end}}> 啟用</span>{{else}}<input type="text" name="{{.Key}}" value="{{.Value}}" placeholder="{{.Hint}}">{{end}}</label>
  {{end}}<button type="submit">儲存設定（寫 .env）</button>
</form>

<h2>MCP 伺服器 <span class="muted">{{.MCPPath}} · 重啟套用</span></h2>
{{if .MCPServers}}<ul class="gitems">{{range .MCPServers}}<li>
  <span><b>{{.Name}}</b> <span class="badge">{{.Type}}</span>{{if .Disabled}} <span class="muted">（停用）</span>{{end}}{{if .HasSecrets}} <span class="muted">🔒 含 env/headers（值遮罩）</span>{{end}}<br><span class="muted">{{.Target}}</span></span>
  <span class="acts">
    <form method="POST" action="/mcp/toggle"><input type="hidden" name="name" value="{{.Name}}"><button type="submit" class="gact ghost">{{if .Disabled}}啟用{{else}}停用{{end}}</button></form>
    <form method="POST" action="/mcp/remove"><input type="hidden" name="name" value="{{.Name}}"><button type="submit" class="gact ghost">移除</button></form>
  </span>
</li>{{end}}</ul>{{else}}<p class="muted">尚無 MCP server（或未設 .mcp.json）。</p>{{end}}
<p class="muted">新增（僅 command／url；需要 env/headers 的 server 請手動編 <code>{{.MCPPath}}</code>，避免祕密經手表單）：</p>
<form method="POST" action="/mcp/add" class="knobs">
  <label>名稱 <span class="muted">英數 + -_</span><input type="text" name="name" placeholder="如 twinkle-hub"></label>
  <label>類型<select name="type"><option value="stdio">stdio（command）</option><option value="http">http（url）</option></select></label>
  <label>command 或 url<input type="text" name="target" placeholder="stdio: npx / http: https://…"></label>
  <label>args <span class="muted">stdio 用，空白分隔</span><input type="text" name="args" placeholder="-y @modelcontextprotocol/server-x"></label>
  <button type="submit">新增 server</button>
</form>

<h2>可調護欄 <span class="muted">就地編輯 · 寫入 .claw/config.json（執行時熱載，免重啟）</span></h2>
<p class="warn">⚠️ 此為【寫入】。bot 每輪重讀；0＝用上方引擎預設。送出值一律 clamp 在安全區間。{{if .KnobsSet}}目前有覆蓋生效。{{else}}目前無覆蓋（全用預設）。{{end}}</p>
<form method="POST" action="/config" class="knobs">
  <label>單次 Run 回合上限 <span class="muted">{{.Limits.MinTurns}}–{{.Limits.MaxTurns}}；0=預設</span>
    <input type="number" name="max_turns" value="{{.Knobs.MaxTurns}}" min="0" max="{{.Limits.MaxTurns}}"></label>
  <label>單輪工具併發上限 <span class="muted">{{.Limits.MinConcurrency}}–{{.Limits.MaxConcurrency}}；0=預設</span>
    <input type="number" name="max_concurrent" value="{{.Knobs.MaxConcurrentTools}}" min="0" max="{{.Limits.MaxConcurrency}}"></label>
  <label>單次 Run 成本熔斷（USD） <span class="muted">{{printf "%.1f" .Limits.MinCostUSD}}–{{printf "%.1f" .Limits.MaxCostUSD}}；0=預設</span>
    <input type="number" step="0.01" name="max_cost" value="{{printf "%.2f" .Knobs.MaxCostUSD}}" min="0" max="{{.Limits.MaxCostUSD}}"></label>
  <button type="submit">儲存護欄</button>
</form>

<h2>自我進化 <span class="muted">提案佇列見 <a href="/governance">Governance</a></span></h2>
<dl class="kv">
  <dt>記憶合成</dt><dd>{{.MemorySynth}}</dd>
  <dt>技能合成</dt><dd>{{.SkillSynth}}</dd>
  <dt>知識圖譜合成</dt><dd>{{.KGSynth}}</dd>
</dl>
`))
