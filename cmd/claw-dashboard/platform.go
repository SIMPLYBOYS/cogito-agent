package main

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
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

	var b bytes.Buffer
	_ = platformTmpl.Execute(&b, d)
	render(w, "Platform（設定檢視）", template.HTML(b.String()))
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
<p class="muted">實際驅動 agent 的平台設定（皆 env 驅動）。祕密只顯示有無、不露值；此頁唯讀，改設定在 bot 端 <code>.env</code>。</p>

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

<h2>自我進化 <span class="muted">提案佇列見 <a href="/governance">Governance</a></span></h2>
<dl class="kv">
  <dt>記憶合成</dt><dd>{{.MemorySynth}}</dd>
  <dt>技能合成</dt><dd>{{.SkillSynth}}</dd>
  <dt>知識圖譜合成</dt><dd>{{.KGSynth}}</dd>
</dl>
`))
