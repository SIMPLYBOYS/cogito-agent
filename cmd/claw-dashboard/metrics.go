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
	}
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

var metricsTmpl = template.Must(template.New("metrics").Parse(`
<p class="muted">所有 session 的用量聚合（來自 session store，不依賴外部 Langfuse）。按平台與模型切片。</p>

<h2>總覽</h2>
<dl class="kv">
  <dt>session 數</dt><dd>{{.Sessions}}</dd>
  <dt>總花費</dt><dd>${{printf "%.4f" .Cost}}</dd>
  <dt>總 token</dt><dd>in {{.PromptTokens}} · out {{.CompletionTokens}}（合計 {{.TotalTok}}）</dd>
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
