package main

import (
	"bytes"
	"html/template"
	"net/http"
	"sort"
	"strings"
)

// metricsData 是 /metrics 頁的唯讀聚合：把所有 session 的用量（cost/token）加總，並按平台、模型切片。
// 資料來源就是 session store（每個 snapshot 的 total_cost_usd / token / model / id），不依賴外部
// Langfuse——這是「不必付費解鎖也能自帶」的按平台/模型分析（見 vault 的 Langfuse 切片討論）。
type metricsData struct {
	Sessions         int
	Cost             float64
	PromptTokens     int
	CompletionTokens int
	Platforms        []metricRow // 按花費新→舊
	Models           []metricRow
	// 以下四項來自逐則 Usage（history），不是 snapshot 的總計欄位。
	//
	// 快取 token 一直都有落盤，只是從來沒人彙總——所以這四個數字對【既有】session
	// 立刻生效，不需要等新資料累積。延遲則是新加的欄位，只有之後跑的回合才有。
	CacheRead   int
	CacheCreate int
	LatP50      int64 // 毫秒
	LatP95      int64
	LatSamples  int // 有耗時資料的回合數；0＝還沒有新資料，前端據此不顯示延遲
}

// CacheHitPct 是快取讀取佔總輸入的比例——判斷 prompt cache 有沒有在work的單一指標。
// 分母含 CacheRead：那些 token 也是輸入，只是計價 0.1x。
func (d metricsData) CacheHitPct() int {
	in := d.PromptTokens + d.CacheRead
	if in == 0 {
		return 0
	}
	return d.CacheRead * 100 / in
}

type metricRow struct {
	Name   string
	Count  int
	Cost   float64
	Tokens int
	Pct    int // 長條寬度 %（相對本組最大花費）
}

// TotalTok 供模板顯示 in+out 合計。
func (d metricsData) TotalTok() int { return d.PromptTokens + d.CompletionTokens }

func (s *server) metrics(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		s.render(w, "Metrics", template.HTML(`<p class="muted">未設 sessions 目錄。啟動時設 `+
			`<code>COGITO_SESSION_DIR</code> 或 <code>-sessions &lt;dir&gt;</code>。</p>`))
		return
	}
	ids, err := s.store.List()
	if err != nil {
		s.render(w, "Metrics", template.HTML(`<p class="muted">讀取 sessions 失敗。</p>`))
		return
	}
	d := metricsData{}
	plat := map[string]*metricRow{}
	model := map[string]*metricRow{}
	var lat []int64
	for _, id := range ids {
		snap, ok, e := s.store.Load(id)
		if e != nil || !ok {
			continue
		}
		d.Sessions++
		d.Cost += snap.TotalCostUSD
		d.PromptTokens += snap.TotalPromptTokens
		d.CompletionTokens += snap.TotalCompletionTokens
		tok := snap.TotalPromptTokens + snap.TotalCompletionTokens

		accum(plat, platformOf(id), snap.TotalCostUSD, tok)
		// 模型：優先用「實際跑過的模型」（CostTracker 記的）；退回 per-channel 覆蓋；再退回早期未記錄。
		m := snap.ModelUsed
		if m == "" {
			m = snap.Model
		}
		if m == "" {
			m = "（早期未記錄）"
		}
		accum(model, m, snap.TotalCostUSD, tok)

		// 逐則 Usage：快取與耗時只存在訊息層，snapshot 的總計欄位沒有它們。
		for _, msg := range snap.History {
			if msg.Usage == nil {
				continue
			}
			d.CacheRead += msg.Usage.CacheReadTokens
			d.CacheCreate += msg.Usage.CacheCreationTokens
			if msg.Usage.LatencyMS > 0 {
				lat = append(lat, msg.Usage.LatencyMS)
			}
		}
	}
	d.LatSamples = len(lat)
	d.LatP50, d.LatP95 = percentile(lat, 50), percentile(lat, 95)
	d.Platforms = rankByCost(plat)
	d.Models = rankByCost(model)

	var b bytes.Buffer
	_ = metricsTmpl.Execute(&b, d)
	s.render(w, "Metrics", template.HTML(b.String()))
}

// platformOf 從 session id 推斷來源平台：IM 用「<platform>:<channel>」慣例；其餘歸本機/dashboard。
func platformOf(id string) string {
	if i := strings.Index(id, ":"); i > 0 {
		switch id[:i] {
		case "slack":
			return "Slack"
		case "telegram":
			return "Telegram"
		default:
			return id[:i]
		}
	}
	if id == operatorSessionID {
		return "Operator（dashboard）"
	}
	return "本機／CLI"
}

func accum(m map[string]*metricRow, key string, cost float64, tok int) {
	row := m[key]
	if row == nil {
		row = &metricRow{Name: key}
		m[key] = row
	}
	row.Count++
	row.Cost += cost
	row.Tokens += tok
}

// rankByCost 依花費新→舊排序，並算出相對最大值的長條寬度 %。
func rankByCost(m map[string]*metricRow) []metricRow {
	var maxCost float64
	for _, r := range m {
		if r.Cost > maxCost {
			maxCost = r.Cost
		}
	}
	rows := make([]metricRow, 0, len(m))
	for _, r := range m {
		if maxCost > 0 {
			r.Pct = int(r.Cost / maxCost * 100)
		}
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Cost > rows[j].Cost })
	return rows
}

// percentile 回第 p 百分位（就地排序）。樣本少時本來就粗——這裡不做插值，
// 因為「大概多慢」就夠用了，而假的精度會讓人以為這是效能量測工具。
func percentile(v []int64, p int) int64 {
	if len(v) == 0 {
		return 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	i := len(v) * p / 100
	if i >= len(v) {
		i = len(v) - 1
	}
	return v[i]
}

var metricsTmpl = template.Must(template.New("metrics").Parse(`
<p class="muted">所有 session 的用量聚合（來自 session store，不依賴外部 Langfuse）。按平台與模型切片。</p>

<h2>總覽</h2>
<dl class="kv">
  <dt>session 數</dt><dd>{{.Sessions}}</dd>
  <dt>總花費</dt><dd>${{printf "%.4f" .Cost}}</dd>
  <dt>總 token</dt><dd>in {{.PromptTokens}} · out {{.CompletionTokens}}（合計 {{.TotalTok}}）</dd>
  <dt>prompt cache</dt><dd>讀 {{.CacheRead}}（0.1x 計價）· 寫 {{.CacheCreate}}（1.25x）·
    <b>命中率 {{.CacheHitPct}}%</b></dd>
  {{if .LatSamples}}<dt>單輪耗時</dt><dd>p50 {{.LatP50}} ms · p95 {{.LatP95}} ms
    <span class="muted">（{{.LatSamples}} 個回合）</span></dd>
  {{else}}<dt>單輪耗時</dt><dd class="muted">尚無資料——耗時是新記錄的欄位，只有之後跑的回合才有</dd>{{end}}
</dl>

<h2>各平台花費</h2>
{{template "bars" .Platforms}}

<h2>各模型花費</h2>
{{template "bars" .Models}}

{{define "bars"}}{{if .}}<div class="bars">
{{range .}}<div class="brow">
  <span class="blabel" title="{{.Name}}">{{.Name}}</span>
  <span class="btrack"><span class="bfill" style="width:{{.Pct}}%"></span></span>
  <span class="bval">${{printf "%.4f" .Cost}} · {{.Count}} run · {{.Tokens}} tok</span>
</div>{{end}}
</div>{{else}}<p class="muted">無資料。</p>{{end}}{{end}}`))
