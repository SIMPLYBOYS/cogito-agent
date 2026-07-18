package main

import (
	"html/template"
	"net/http"
	"runtime"
)

// newServer 組出 operator dashboard 的路由（用自己的 mux，不碰 http.DefaultServeMux——避免任何被 import
// 的套件如 pprof/expvar 偷偷往預設 mux 塞 handler）。本階段 C0 只有骨架 + Status；Runs（C1）/ Governance
// （C2-檢視）先放 placeholder，各自 phase 再填。
func newServer() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleHome)
	mux.HandleFunc("/status", handleStatus)
	mux.HandleFunc("/runs", handleStub("Runs（執行流視覺化）", "C1：session 列表 + run-tree viewer"))
	mux.HandleFunc("/governance", handleStub("Governance（治理檢視）", "C2：待審批 / 提案佇列 / 授權用戶——檢視（動作留 chat）"))
	return mux
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	render(w, "首頁", homeBody)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	render(w, "Status", template.HTML(
		`<dl class="kv">`+
			`<dt>服務</dt><dd>cogito operator dashboard（唯讀）</dd>`+
			`<dt>Go</dt><dd>`+template.HTMLEscapeString(runtime.Version())+`</dd>`+
			`<dt>存取模式</dt><dd>綁 loopback、無認證（remote 需 auth，尚未實作）</dd>`+
			`<dt>寫入動作</dt><dd>留 chat（沿用 IM 身分）——本面板不做寫入</dd>`+
			`</dl>`))
}

// handleStub 回一個「此頁在 phase X 才填」的佔位頁，讓導覽現在就通、但誠實標示未實作。
func handleStub(title, note string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		render(w, title, template.HTML(`<p class="muted">`+template.HTMLEscapeString(note)+`</p>`))
	}
}

const homeBody template.HTML = `<p>cogito 的維運面板。本階段唯讀、僅本機。</p>
<ul>
  <li><a href="/runs">Runs</a> — 一次 query 的執行流（主 agent + 子 agent 協同）</li>
  <li><a href="/governance">Governance</a> — 待審批 / 提案 / 授權用戶（檢視）</li>
  <li><a href="/status">Status</a> — 服務狀態</li>
</ul>`

// render 用單一 base layout 包正文。html/template 對 body 以 template.HTML 傳入（呼叫端須自行確保已跳脫），
// 版面文字走自動跳脫。CSP 設嚴（無外部資源、無 inline script），深淺色自適應。
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
  :root { color-scheme: light dark; --fg:#1a1a1a; --bg:#fdfdfc; --mut:#777; --acc:#b24a32; --line:#e5e3df; --card:#fff; }
  @media (prefers-color-scheme: dark) { :root { --fg:#e7d8c9; --bg:#141110; --mut:#8a7566; --acc:#e8734a; --line:#2a2320; --card:#1b1512; } }
  * { box-sizing:border-box; } body { margin:0; font:15px/1.6 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace; color:var(--fg); background:var(--bg); }
  header { border-bottom:1px solid var(--line); padding:14px 20px; display:flex; gap:18px; align-items:baseline; }
  header b { color:var(--acc); } header nav a { color:var(--fg); text-decoration:none; margin-right:14px; opacity:.8; }
  header nav a:hover { opacity:1; text-decoration:underline; }
  main { padding:20px; max-width:900px; } h1 { font-size:19px; margin:0 0 14px; }
  a { color:var(--acc); } .muted, .mut { color:var(--mut); }
  ul { padding-left:20px; } li { margin:4px 0; }
  dl.kv { display:grid; grid-template-columns:auto 1fr; gap:6px 16px; } dl.kv dt { color:var(--mut); } dl.kv dd { margin:0; }
</style></head>
<body>
<header><b>cogito ops</b> <nav><a href="/runs">Runs</a><a href="/governance">Governance</a><a href="/status">Status</a></nav></header>
<main><h1>{{.Title}}</h1>{{.Body}}</main>
</body></html>`))
