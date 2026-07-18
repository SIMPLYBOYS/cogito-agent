package main

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/replay"
)

// server 持有 session store（可為 nil＝未設 sessions 目錄）、生效目錄、workspace 根（找 .claw/ 用）。全唯讀。
type server struct {
	store     ctxpkg.SessionStore
	dir       string
	workspace string
}

// newServer 組出 operator dashboard 的路由。用自己的 mux（不碰 http.DefaultServeMux——避免任何被
// import 的套件如 pprof/expvar 偷塞 handler）。用 Go 1.22 路由樣式（`/runs/{id}`）。
func newServer(store ctxpkg.SessionStore, dir, workspace string) http.Handler {
	s := &server{store: store, dir: dir, workspace: workspace}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /status", s.status)
	mux.HandleFunc("GET /runs", s.runsList)
	mux.HandleFunc("GET /runs/{id}", s.runDetail)
	mux.HandleFunc("GET /governance", s.governance)
	return mux
}

func (s *server) home(w http.ResponseWriter, r *http.Request) { render(w, "首頁", homeBody) }

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	dir := s.dir
	if dir == "" {
		dir = "（未設；設 COGITO_SESSION_DIR 或 -sessions 才看得到 runs）"
	}
	n := "—"
	if s.store != nil {
		if ids, err := s.store.List(); err == nil {
			n = strconv.Itoa(len(ids))
		}
	}
	render(w, "Status", template.HTML(
		`<dl class="kv">`+
			`<dt>服務</dt><dd>cogito operator dashboard（唯讀）</dd>`+
			`<dt>Go</dt><dd>`+template.HTMLEscapeString(runtime.Version())+`</dd>`+
			`<dt>sessions 目錄</dt><dd>`+template.HTMLEscapeString(dir)+`</dd>`+
			`<dt>session 數</dt><dd>`+n+`</dd>`+
			`<dt>存取模式</dt><dd>綁 loopback、無認證（remote 需 auth，尚未實作）</dd>`+
			`<dt>寫入動作</dt><dd>留 chat（沿用 IM 身分）——本面板不做寫入</dd>`+
			`</dl>`))
}

type runRow struct {
	ID, Link, Query, Updated string
	Turns                    int
	Cost                     float64
	Sub, Running             bool
}

// runsList 列出所有 session（讀每個 snapshot、重建 Run 取摘要），依更新時間新→舊。
func (s *server) runsList(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		render(w, "Runs", template.HTML(`<p class="muted">未設 sessions 目錄。啟動時設 `+
			`<code>COGITO_SESSION_DIR</code> 或 <code>-sessions &lt;dir&gt;</code>。</p>`))
		return
	}
	ids, err := s.store.List()
	if err != nil {
		render(w, "Runs", template.HTML(`<p class="muted">讀取 sessions 失敗。</p>`))
		return
	}
	rows := make([]runRow, 0, len(ids))
	for _, id := range ids {
		snap, ok, e := s.store.Load(id)
		if e != nil || !ok {
			continue
		}
		run := replay.Build(id, snap.History, metaOf(snap))
		rows = append(rows, runRow{
			ID: id, Link: "/runs/" + url.PathEscape(id), Query: run.Query,
			Updated: snap.UpdatedAt, Turns: len(run.Turns), Cost: snap.TotalCostUSD,
			Sub: run.HasSubagent, Running: snap.Running,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Updated > rows[j].Updated }) // RFC3339 字串序＝時間序

	var b bytes.Buffer
	_ = runsListTmpl.Execute(&b, rows)
	render(w, "Runs", template.HTML(b.String()))
}

var runsListTmpl = template.Must(template.New("runs").Parse(`{{if not .}}<p class="muted">目前沒有 session。</p>{{else}}
<table class="runs"><thead><tr><th>session</th><th>任務</th><th>步</th><th>成本</th><th></th><th>更新</th></tr></thead><tbody>
{{range .}}<tr>
  <td><a href="{{.Link}}">{{.ID}}</a></td>
  <td class="q" title="{{.Query}}">{{.Query}}</td>
  <td>{{.Turns}}</td>
  <td>${{printf "%.4f" .Cost}}</td>
  <td>{{if .Sub}}<span class="badge">subagent</span>{{end}}{{if .Running}}<span class="badge">執行中</span>{{end}}</td>
  <td class="muted">{{.Updated}}</td>
</tr>{{end}}
</tbody></table>{{end}}`))

// runDetail 渲染單一 session 的執行樹。
func (s *server) runDetail(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	snap, ok, err := s.store.Load(id)
	if err != nil || !ok {
		render(w, "Run 不存在", template.HTML(`<p class="muted">找不到這個 session。<a href="/runs">← 回列表</a></p>`))
		return
	}
	run := replay.Build(id, snap.History, metaOf(snap))
	body := template.HTML(`<p class="muted"><a href="/runs">← 回列表</a> · session `+
		template.HTMLEscapeString(id)+`</p>`) + replay.Fragment(run)
	render(w, "Run", body)
}

// metaOf 把 SessionSnapshot 映射成 replay.Meta（replay 不依賴 session store，故由這裡橋接）。
func metaOf(snap *ctxpkg.SessionSnapshot) replay.Meta {
	return replay.Meta{
		UpdatedAt:        snap.UpdatedAt,
		Cost:             snap.TotalCostUSD,
		PromptTokens:     snap.TotalPromptTokens,
		CompletionTokens: snap.TotalCompletionTokens,
		Model:            snap.Model,
		Goal:             snap.Goal,
		Running:          snap.Running,
	}
}

const homeBody template.HTML = `<p>cogito 的維運面板。本階段唯讀、僅本機。</p>
<ul>
  <li><a href="/runs">Runs</a> — 一次 query 的執行流（主 agent + 子 agent 協同）</li>
  <li><a href="/governance">Governance</a> — 待審批 / 提案 / 授權用戶（檢視）</li>
  <li><a href="/status">Status</a> — 服務狀態</li>
</ul>`

// render 用單一 base layout 包正文。CSP 嚴（無外部資源、無 inline script；style 允許 inline 供 base 與
// run 片段的 <style>）。深淺色自適應。body 以 template.HTML 傳入，呼叫端須確保已跳脫。
func render(w http.ResponseWriter, title string, body template.HTML) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = baseTmpl.Execute(w, map[string]any{"Title": title, "Body": body})
}

var baseTmpl = template.Must(template.New("base").Parse(`<!doctype html>
<html lang="zh-Hant"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · cogito ops</title>
<style>
  :root { color-scheme: light dark; --fg:#1a1a1a; --bg:#fdfdfc; --mut:#777; --acc:#b24a32; --line:#e5e3df; }
  @media (prefers-color-scheme: dark) { :root { --fg:#e7d8c9; --bg:#141110; --mut:#8a7566; --acc:#e8734a; --line:#2a2320; } }
  * { box-sizing:border-box; } body { margin:0; font:15px/1.6 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace; color:var(--fg); background:var(--bg); }
  header { border-bottom:1px solid var(--line); padding:14px 20px; display:flex; gap:18px; align-items:baseline; }
  header b { color:var(--acc); } header nav a { color:var(--fg); text-decoration:none; margin-right:14px; opacity:.8; }
  header nav a:hover { opacity:1; text-decoration:underline; }
  main { padding:20px; max-width:960px; } h1 { font-size:19px; margin:0 0 14px; }
  a { color:var(--acc); } .muted { color:var(--mut); } code { color:var(--mut); }
  ul { padding-left:20px; } li { margin:4px 0; }
  dl.kv { display:grid; grid-template-columns:auto 1fr; gap:6px 16px; } dl.kv dt { color:var(--mut); } dl.kv dd { margin:0; }
  h2 { font-size:16px; margin:22px 0 8px; border-bottom:1px solid var(--line); padding-bottom:4px; } h3 { font-size:14px; margin:14px 0 6px; }
  pre.prev { white-space:pre-wrap; word-break:break-word; font-size:12.5px; color:var(--mut); border-left:2px solid var(--line); padding:6px 10px; max-height:280px; overflow:auto; }
  table.runs { width:100%; border-collapse:collapse; font-size:13.5px; } table.runs th { text-align:left; color:var(--mut); font-weight:normal; border-bottom:1px solid var(--line); padding:6px 8px; }
  table.runs td { border-bottom:1px solid var(--line); padding:6px 8px; vertical-align:top; } table.runs td.q { max-width:360px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .badge { color:var(--acc); border:1px solid var(--acc); border-radius:4px; padding:0 5px; font-size:11px; margin-left:4px; }
</style></head>
<body>
<header><b>cogito ops</b> <nav><a href="/runs">Runs</a><a href="/governance">Governance</a><a href="/status">Status</a></nav></header>
<main><h1>{{.Title}}</h1>{{.Body}}</main>
</body></html>`))
