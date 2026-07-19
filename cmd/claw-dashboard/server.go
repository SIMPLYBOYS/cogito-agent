package main

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"sync/atomic"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/replay"
)

// server 持有 session store（可為 nil＝未設 sessions 目錄）、生效目錄、workspace 根（找 .claw/ 用），
// 及可選的 chat（nil＝未啟用 operator chat，面板維持唯讀）。除 chat 外全唯讀。
type server struct {
	store     ctxpkg.SessionStore
	dir       string
	workspace string
	chat      *chatRunner
	flash     atomic.Value // string：上次寫入動作（放行／設定）的結果，GET 時顯示一次即清
}

// newServer 組出 operator dashboard 的路由。用自己的 mux（不碰 http.DefaultServeMux——避免任何被
// import 的套件如 pprof/expvar 偷塞 handler）。用 Go 1.22 路由樣式（`/runs/{id}`）。chat 為 nil 時
// /chat 仍註冊，但顯示「未啟用」提示（唯讀）。
func newServer(store ctxpkg.SessionStore, dir, workspace string, chat *chatRunner) http.Handler {
	s := &server{store: store, dir: dir, workspace: workspace, chat: chat}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /status", s.status)
	mux.HandleFunc("GET /runs", s.runsList)
	mux.HandleFunc("GET /runs/{id}", s.runDetail)
	mux.HandleFunc("GET /governance", s.governance)
	mux.HandleFunc("POST /governance/apply-config", s.govApplyConfig)
	mux.HandleFunc("POST /governance/apply-memory", s.govApplyMemory)
	mux.HandleFunc("POST /governance/discard-memory", s.govDiscardMemory)
	mux.HandleFunc("POST /governance/promote-skill", s.govPromoteSkill)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /platform", s.platform)
	mux.HandleFunc("POST /config", s.configSave)
	mux.HandleFunc("POST /env-config", s.envConfigSave)
	mux.HandleFunc("GET /platform.js", s.platformJS)
	mux.HandleFunc("GET /secret/reveal", s.secretReveal)
	mux.HandleFunc("POST /secret", s.secretSave)
	mux.HandleFunc("GET /mcp/secret/reveal", s.mcpSecretReveal)
	mux.HandleFunc("POST /mcp/secret", s.mcpSecretSave)
	mux.HandleFunc("POST /mcp/add", s.mcpAdd)
	mux.HandleFunc("POST /mcp/edit", s.mcpEdit)
	mux.HandleFunc("POST /mcp/remove", s.mcpRemove)
	mux.HandleFunc("POST /mcp/toggle", s.mcpToggle)
	mux.HandleFunc("GET /chat", s.chatGet)
	mux.HandleFunc("POST /chat", s.chatPost)
	mux.HandleFunc("POST /chat/reset", s.chatReset)
	mux.HandleFunc("GET /chat.js", s.chatJS)
	mux.HandleFunc("GET /chat/stream", s.chatStream)
	return mux
}

func (s *server) home(w http.ResponseWriter, r *http.Request) { s.render(w, "首頁", homeBody) }

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
	writeMode := `治理放行 / 護欄調參可就地寫入（CSRF + clamp/gate 保護）；agent 執行（operator chat）未啟用（設 COGITO_DASH_CHAT=1）`
	if s.chat != nil {
		writeMode = `⚠️ operator chat 已啟用——可就地驅動 agent（跑 bash／寫檔）；另含治理放行 / 護欄調參寫入。受 loopback + CSRF 保護`
	}
	s.render(w, "Status", template.HTML(
		`<dl class="kv">`+
			`<dt>服務</dt><dd>cogito operator dashboard</dd>`+
			`<dt>Go</dt><dd>`+template.HTMLEscapeString(runtime.Version())+`</dd>`+
			`<dt>sessions 目錄</dt><dd>`+template.HTMLEscapeString(dir)+`</dd>`+
			`<dt>session 數</dt><dd>`+n+`</dd>`+
			`<dt>存取模式</dt><dd>綁 loopback、無認證（remote 需 auth，尚未實作）</dd>`+
			`<dt>寫入模式</dt><dd>`+writeMode+`</dd>`+
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
		s.render(w, "Runs", template.HTML(`<p class="muted">未設 sessions 目錄。啟動時設 `+
			`<code>COGITO_SESSION_DIR</code> 或 <code>-sessions &lt;dir&gt;</code>。</p>`))
		return
	}
	ids, err := s.store.List()
	if err != nil {
		s.render(w, "Runs", template.HTML(`<p class="muted">讀取 sessions 失敗。</p>`))
		return
	}
	rows := make([]runRow, 0, len(ids))
	for _, id := range ids {
		snap, ok, e := s.store.Load(id)
		if e != nil || !ok {
			continue
		}
		run := replay.Build(id, snap.History, metaOf(snap), "") // 列表不載子深度（只需計數/是否有 subagent）
		rows = append(rows, runRow{
			ID: id, Link: "/runs/" + url.PathEscape(id), Query: run.Query,
			Updated: snap.UpdatedAt, Turns: len(run.Turns), Cost: snap.TotalCostUSD,
			Sub: run.HasSubagent, Running: snap.Running,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Updated > rows[j].Updated }) // RFC3339 字串序＝時間序

	var b bytes.Buffer
	_ = runsListTmpl.Execute(&b, rows)
	s.render(w, "Runs", template.HTML(b.String()))
}

var runsListTmpl = template.Must(template.New("runs").Parse(`{{if not .}}<p class="muted">目前沒有 session。</p>{{else}}
<table class="runs"><thead><tr><th>session</th><th>任務</th><th>步</th><th>成本</th><th></th><th>更新</th></tr></thead><tbody>
{{range .}}<tr>
  <td><a href="{{.Link}}">{{.ID}}</a></td>
  <td class="q" title="{{.Query}}">{{.Query}}</td>
  <td class="num">{{.Turns}}</td>
  <td class="num">${{printf "%.4f" .Cost}}</td>
  <td>{{if .Sub}}<span class="pill sub">subagent</span>{{end}}{{if .Running}}<span class="pill run">執行中</span>{{end}}</td>
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
		s.render(w, "Run 不存在", template.HTML(`<p class="muted">找不到這個 session。<a href="/runs">← 回列表</a></p>`))
		return
	}
	run := replay.Build(id, snap.History, metaOf(snap), snap.WorkDir) // 詳情：載入 subagent 內部深度
	body := template.HTML(`<p class="muted"><a href="/runs">← 回列表</a> · session `+
		template.HTMLEscapeString(id)+`</p>`) + replay.Fragment(run)
	s.render(w, "Run", body)
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

const homeBody template.HTML = `<p class="muted">cogito 的維運面板 · 本階段唯讀、僅本機</p>
<div class="cards">
  <a class="card" href="/chat"><span class="ct">Chat →</span><span class="cd">內嵌 operator chat：就地驅動 agent 跑任務（寫入；opt-in COGITO_DASH_CHAT=1）</span></a>
  <a class="card" href="/runs"><span class="ct">Runs →</span><span class="cd">一次 query 的完整執行流：主 agent 的 ReAct 迴圈與子 agent 協同，逐步可展開</span></a>
  <a class="card" href="/metrics"><span class="ct">Metrics →</span><span class="cd">用量觀測：總花費、各平台／各模型 token 與成本切片（自帶，不依賴 Langfuse）</span></a>
  <a class="card" href="/governance"><span class="ct">Governance →</span><span class="cd">技能／記憶／調參提案佇列與授權名單——就地放行（晉升技能、套用記憶／調參）</span></a>
  <a class="card" href="/platform"><span class="ct">Platform →</span><span class="cd">provider／模型、通道綁定、可觀測性、執行護欄（設定檢視）</span></a>
  <a class="card" href="/status"><span class="ct">Status →</span><span class="cd">服務狀態、sessions 目錄、存取模式</span></a>
</div>`

// CSP：預設嚴（無外部資源、無 inline script）。chat 頁額外放寬 script-src/connect-src 'self'——SSE
// 串流需要（EventSource + /chat.js）；仍限同源、無 inline script、無外部主機。只有 chat 一頁放寬。
const (
	cspStrict = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'self'"
	cspChat   = "default-src 'none'; style-src 'unsafe-inline'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'"
)

// render 用單一 base layout 包正文（嚴 CSP）。深淺色自適應。body 以 template.HTML 傳入，呼叫端須確保已跳脫。
func (s *server) render(w http.ResponseWriter, title string, body template.HTML) {
	s.writeLayout(w, title, body, cspStrict)
}

// renderChat 同 render 但用放寬的 chat CSP（供 SSE 串流）。
func (s *server) renderChat(w http.ResponseWriter, title string, body template.HTML) {
	s.writeLayout(w, title, body, cspChat)
}

func (s *server) writeLayout(w http.ResponseWriter, title string, body template.HTML, csp string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = baseTmpl.Execute(w, map[string]any{"Title": title, "Body": body, "Write": s.chat != nil})
}

var baseTmpl = template.Must(template.New("base").Parse(`<!doctype html>
<html lang="zh-Hant"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · cogito ops</title>
<style>
  :root {
    color-scheme: light dark;
    --bg:#faf7f2; --bg2:#f3ede4; --fg:#241d18; --mut:#8a7362; --line:#e4ddd2;
    --acc:#b24a32; --acc2:#a8641f; --ok:#5e8a4a; --glow:rgba(178,74,50,.14);
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg:#14100e; --bg2:#1b1512; --fg:#ece0d4; --mut:#9c8676; --line:#2c2420;
      --acc:#e8734a; --acc2:#e0a45a; --ok:#86b06e; --glow:rgba(232,115,74,.16);
    }
  }
  * { box-sizing:border-box; }
  body { margin:0; color:var(--fg); background:var(--bg);
    font:14.5px/1.65 ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace; font-variant-ligatures:none; }
  a { color:var(--acc); text-decoration:none; } a:hover { text-decoration:underline; }
  .muted { color:var(--mut); }
  .warn { color:var(--acc); }
  code { font:inherit; font-size:.92em; color:var(--acc2); background:color-mix(in srgb,var(--acc2) 12%,transparent);
    border:1px solid var(--line); border-radius:4px; padding:0 5px; }
  /* masthead */
  header { position:sticky; top:0; z-index:5; display:flex; align-items:center; gap:20px;
    padding:12px 22px; border-bottom:1px solid var(--line);
    background:color-mix(in srgb,var(--bg) 86%,transparent); -webkit-backdrop-filter:blur(9px); backdrop-filter:blur(9px); }
  header .brand { display:flex; align-items:center; gap:10px; }
  header .mark { width:18px; height:18px; border-radius:4px;
    background:linear-gradient(135deg,var(--acc2),var(--acc)); box-shadow:0 0 15px var(--glow); }
  header .wm { font-weight:700; letter-spacing:.16em; text-transform:uppercase; font-size:14px; }
  header .wm em { color:var(--acc); font-style:normal; }
  header nav { display:flex; gap:3px; }
  header nav a { color:var(--mut); padding:4px 11px; border-radius:6px; letter-spacing:.03em; }
  header nav a:hover { color:var(--fg); background:var(--bg2); text-decoration:none; }
  header .env { margin-left:auto; display:flex; align-items:center; gap:7px; color:var(--mut);
    font-size:11.5px; letter-spacing:.05em; }
  header .env .dot { width:7px; height:7px; border-radius:50%; background:var(--ok); box-shadow:0 0 8px var(--ok); }
  header .env .dot.write { background:var(--acc); box-shadow:0 0 8px var(--acc); }
  main { padding:28px 22px 64px; max-width:1000px; margin:0 auto; }
  h1 { font-size:20px; font-weight:700; margin:0 0 6px; text-wrap:balance; }
  h2 { font-size:12px; text-transform:uppercase; letter-spacing:.13em; color:var(--mut); font-weight:600;
    margin:32px 0 12px; padding-bottom:7px; border-bottom:1px solid var(--line); }
  h3 { font-size:13.5px; font-weight:600; margin:20px 0 8px; }
  h3 .muted { font-weight:400; }
  ul { padding-left:20px; } li { margin:5px 0; }
  dl.kv { display:grid; grid-template-columns:auto 1fr; gap:9px 20px; margin:0; }
  dl.kv dt { color:var(--mut); } dl.kv dd { margin:0; }
  /* route cards */
  .cards { display:grid; gap:12px; grid-template-columns:repeat(auto-fit,minmax(230px,1fr)); margin:20px 0; }
  .card { display:flex; flex-direction:column; gap:5px; border:1px solid var(--line); border-radius:10px;
    padding:15px 16px; background:var(--bg2); color:var(--fg);
    transition:border-color .15s,transform .15s,box-shadow .15s; }
  .card:hover { border-color:var(--acc); box-shadow:0 5px 22px var(--glow); transform:translateY(-1px); text-decoration:none; }
  .card .ct { font-weight:700; letter-spacing:.03em; }
  .card .cd { color:var(--mut); font-size:12.5px; }
  /* preview */
  pre.prev { white-space:pre-wrap; word-break:break-word; font-size:12.5px; color:var(--mut); background:var(--bg2);
    border:1px solid var(--line); border-left:2px solid var(--acc2); border-radius:7px; padding:10px 12px;
    max-height:300px; overflow:auto; }
  /* runs table */
  table.runs { width:100%; border-collapse:collapse; font-size:13px; }
  table.runs th { text-align:left; color:var(--mut); font-weight:500; font-size:10.5px; text-transform:uppercase;
    letter-spacing:.09em; border-bottom:1px solid var(--line); padding:8px 10px; }
  table.runs td { border-bottom:1px solid var(--line); padding:9px 10px; vertical-align:top; }
  table.runs tbody tr { transition:background .12s; }
  table.runs tbody tr:hover { background:var(--bg2); }
  table.runs td.q { max-width:360px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  table.runs td.num { text-align:right; font-variant-numeric:tabular-nums; color:var(--mut); }
  table.runs td a { font-weight:600; }
  /* 護欄編輯表單（零 JS） */
  form.knobs { display:flex; flex-direction:column; gap:13px; max-width:540px; margin:6px 0; }
  form.knobs label { display:flex; flex-direction:column; gap:4px; font-size:13px; }
  form.knobs input { font:inherit; color:var(--fg); background:var(--bg2); border:1px solid var(--line); border-radius:6px; padding:6px 10px; max-width:200px; }
  form.knobs input:focus { outline:none; border-color:var(--acc); }
  form.knobs input[type=checkbox] { width:auto; max-width:none; }
  form.knobs .tog { display:inline-flex; align-items:center; gap:6px; font-size:13px; }
  form.knobs select { font:inherit; color:var(--fg); background:var(--bg2); border:1px solid var(--line); border-radius:6px; padding:6px 10px; max-width:220px; }
  ul.gitems .acts { display:flex; gap:8px; flex:none; }
  /* 金鑰／祕密（眼睛顯示 + 輪替） */
  .secrets { display:flex; flex-direction:column; }
  .secret { border-bottom:1px solid var(--line); padding:8px 0; display:flex; align-items:center; gap:12px; flex-wrap:wrap; font-size:13px; }
  .secret .sk { min-width:210px; }
  .secret .sv { color:var(--mut); word-break:break-all; }
  .secret .eye { background:transparent; border:1px solid var(--line); border-radius:5px; cursor:pointer; padding:0 7px; font-size:13px; }
  .secret .eye:hover { border-color:var(--acc); }
  .secret details { flex-basis:100%; }
  /* MCP server 列表（可展開編輯） */
  .mcplist { display:flex; flex-direction:column; }
  .mcpitem { border-bottom:1px solid var(--line); padding:9px 0; }
  .mcprow { display:flex; justify-content:space-between; align-items:center; gap:12px; }
  .mcprow .acts { display:flex; gap:8px; flex:none; }
  .mcpedit { margin-top:7px; }
  .mcpedit > summary { cursor:pointer; color:var(--acc2); font-size:12px; list-style:none; }
  .mcpedit > summary::-webkit-details-marker { display:none; }
  .mcpedit > summary::before { content:"▸ "; }
  .mcpedit[open] > summary::before { content:"▾ "; }
  .mcpedit form.knobs { margin:10px 0 4px; padding-left:12px; border-left:2px solid var(--line); max-width:560px; }
  .mcpedit .hint { color:var(--mut); font-size:11.5px; }
  form.knobs button { align-self:flex-start; font:inherit; font-weight:700; letter-spacing:.03em; color:#fff; background:var(--acc); border:none; border-radius:8px; padding:7px 18px; cursor:pointer; }
  form.knobs button:hover { filter:brightness(1.08); }
  /* metrics 長條圖（純 CSS，零 JS） */
  .bars { display:flex; flex-direction:column; gap:9px; margin:4px 0; }
  .brow { display:grid; grid-template-columns:150px 1fr auto; align-items:center; gap:14px; font-size:12.5px; }
  .brow .blabel { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .brow .btrack { height:14px; background:var(--bg2); border:1px solid var(--line); border-radius:4px; overflow:hidden; }
  .brow .bfill { display:block; height:100%; background:linear-gradient(90deg,var(--acc2),var(--acc)); }
  .brow .bval { color:var(--mut); font-variant-numeric:tabular-nums; white-space:nowrap; }
  /* 通用 banner（governance flash 等；chat 內另有 scoped 版） */
  .banner { border:1px solid var(--acc); border-radius:7px; padding:8px 12px; margin:0 0 14px; color:var(--acc); font-size:13px; }
  .banner.done { color:var(--ok); border-color:var(--ok); }
  /* governance 放行動作 */
  ul.gitems { list-style:none; padding:0; }
  ul.gitems li { display:flex; justify-content:space-between; align-items:center; gap:12px; border-bottom:1px solid var(--line); padding:8px 0; }
  ul.gitems form, .grow form { margin:0; }
  .grow { display:flex; align-items:center; gap:12px; margin:9px 0 2px; }
  .grow .hint { color:var(--mut); font-size:11.5px; }
  button.gact { font:inherit; font-size:12.5px; font-weight:600; letter-spacing:.02em; color:#fff; background:var(--acc); border:none; border-radius:6px; padding:4px 14px; cursor:pointer; }
  button.gact:hover { filter:brightness(1.08); }
  button.gact.ghost { color:var(--mut); background:transparent; border:1px solid var(--line); }
  button.gact.ghost:hover { color:var(--acc); border-color:var(--acc); filter:none; }
  .badge, .pill { display:inline-block; font-size:10.5px; letter-spacing:.03em; border-radius:5px; padding:1px 7px; margin-left:4px; }
  .pill.sub { color:var(--acc); border:1px solid var(--acc); }
  .pill.run { color:var(--acc2); border:1px solid var(--acc2); animation:pulse 1.8s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity:.4; } }
  @media (prefers-reduced-motion: reduce) { .pill.run { animation:none; } }
</style></head>
<body>
<header>
  <span class="brand"><span class="mark"></span><span class="wm">cogito<em> ops</em></span></span>
  <nav><a href="/chat">chat</a><a href="/runs">runs</a><a href="/metrics">metrics</a><a href="/governance">governance</a><a href="/platform">platform</a><a href="/status">status</a></nav>
  <span class="env"><span class="dot{{if .Write}} write{{end}}"></span>loopback{{if .Write}} · operator（可寫入）{{end}}</span>
</header>
<main><h1>{{.Title}}</h1>{{.Body}}</main>
</body></html>`))
