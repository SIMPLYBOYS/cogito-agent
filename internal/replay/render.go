package replay

import (
	"bytes"
	"html/template"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// Fragment 把 Run 渲染成一段自包含的 HTML（含自己的 <style>，不依賴外部 CSS——這樣同一個渲染核心
// 未來也能包成獨立的靜態 artifact）。顏色優先吃外層 dashboard 的 token（--acc/--mut/…），缺了才用
// fallback，所以嵌進 dashboard 時與整體同一套 ember-console 語言，單獨渲染也不裸。用 native <details>
// 折疊，零 JS。所有動態文字經 html/template 自動跳脫（來自工具輸出/模型文字，視為不受信）。
func Fragment(run Run) template.HTML {
	var b bytes.Buffer
	if err := fragmentTmpl.Execute(&b, run); err != nil {
		return template.HTML("<p>（run 渲染失敗）</p>")
	}
	return template.HTML(b.String())
}

var fragmentTmpl = template.Must(template.New("run").Funcs(template.FuncMap{
	// summary 裡的任務標題：截短到一行——全文在卡片內的任務欄，摺疊列只要認得出是哪件事。
	"trunc": func(s string) string { return schema.TruncRunes(s, 72, "…") },
}).Parse(`<style>
  .run { --a:var(--acc,#e8734a); --a2:var(--acc2,#e0a45a); --m:var(--mut,#8a7362); --ln:var(--line,#2c2420); --p:var(--bg2,#1b1512); --g:var(--glow,rgba(232,115,74,.14)); }
  /* 任務卡 */
  .run .q { background:var(--p); border:1px solid var(--ln); border-left:3px solid var(--a); border-radius:8px; padding:11px 14px; margin-bottom:13px; }
  .run .q .lbl { display:block; color:var(--m); font-size:10.5px; text-transform:uppercase; letter-spacing:.14em; margin-bottom:4px; }
  /* 摘要 chips */
  .run .rmeta { font-size:12px; margin-bottom:22px; display:flex; flex-wrap:wrap; gap:7px; align-items:center; }
  .run .rmeta .chip { color:var(--m); background:var(--p); border:1px solid var(--ln); border-radius:5px; padding:1px 8px; font-variant-numeric:tabular-nums; }
  .run .rmeta .badge { color:var(--a); border:1px solid var(--a); border-radius:5px; padding:1px 8px; letter-spacing:.02em; }
  .run .rmeta .solo { color:var(--m); }
  /* 軌道時間線 */
  .run .turn { border-left:2px solid var(--ln); padding:0 0 15px 18px; margin-left:6px; position:relative; }
  .run .turn::before { content:""; position:absolute; left:-6px; top:4px; width:9px; height:9px; border-radius:50%; background:var(--bg,#14100e); border:2px solid var(--m); }
  .run .turn:last-child { border-left-color:transparent; }
  .run .turn.answer { border-left-color:var(--a); }
  .run .turn.answer::before { background:var(--a); border-color:var(--a); box-shadow:0 0 9px var(--g); }
  .run .turn .tn { color:var(--m); font-size:11px; font-variant-numeric:tabular-nums; letter-spacing:.05em; margin-right:8px; }
  .run .turn .ans-title { color:var(--a); font-weight:700; letter-spacing:.02em; }
  /* 思考／動作 */
  .run details { margin:5px 0; }
  .run summary { cursor:pointer; color:var(--m); font-size:12.5px; list-style:none; }
  .run summary::-webkit-details-marker { display:none; }
  .run summary::before { content:"▸ "; color:var(--a2); }
  .run details[open]>summary::before { content:"▾ "; }
  .run .content { white-space:pre-wrap; word-break:break-word; margin:5px 0 3px 14px; font-size:13.5px; }
  .run .think .content { color:var(--fg,#ece0d4); border-left:2px solid var(--a2); padding-left:10px; margin-left:2px; }
  .run .action { margin:8px 0; }
  .run .action .am { color:var(--a2); margin-right:6px; }
  .run .action b { letter-spacing:.01em; }
  .run pre.args { white-space:pre-wrap; word-break:break-word; margin:4px 0 0 14px; font-size:12px; color:var(--m); background:var(--p); border:1px solid var(--ln); border-radius:6px; padding:7px 10px; max-height:220px; overflow:auto; }
  .run .usage { color:var(--m); font-size:10.5px; margin-top:7px; letter-spacing:.03em; font-variant-numeric:tabular-nums; }
  .run .usage .cost { color:var(--a2); }
  .run .note { color:var(--m); font-size:12.5px; font-style:italic; margin:7px 0; opacity:.85; }
  /* 任務卡：一個 session 累積多個任務時，各自摺疊——只有最新的預設展開 */
  .run .taskgrp { border:1px solid var(--ln); border-radius:8px; padding:8px 13px; margin-bottom:12px; background:var(--p); }
  .run .taskgrp>summary { display:flex; gap:10px; align-items:baseline; flex-wrap:wrap; font-size:13px; color:var(--fg,#ece0d4); padding:2px 0; }
  .run .taskgrp>summary .tchips { margin-left:auto; color:var(--m); font-size:11.5px; font-variant-numeric:tabular-nums; white-space:nowrap; }
  .run .taskgrp[open]>summary { border-bottom:1px solid var(--ln); padding-bottom:8px; margin-bottom:10px; }
  .run .taskgrp .q { margin:2px 0 12px; }
  /* fan-out：一輪派出多個子 agent → 扇形卡片（一節點 → 多分支） */
  .run .fan { margin:6px 0 4px 2px; }
  .run .fanroot { color:var(--a2); font-size:12px; letter-spacing:.02em; margin-bottom:8px; }
  .run .fancards { display:flex; flex-wrap:wrap; gap:10px; }
  .run .fcard { flex:1 1 200px; min-width:180px; background:var(--p); border:1px solid var(--ln); border-top:2px solid var(--a); border-radius:8px; padding:0; }
  .run .fcard>summary { cursor:pointer; list-style:none; display:flex; align-items:baseline; justify-content:space-between; gap:8px; padding:9px 12px; }
  .run .fcard>summary::-webkit-details-marker { display:none; }
  .run .fcard>summary::before { content:"▸ "; color:var(--a2); }
  .run .fcard[open]>summary { border-bottom:1px solid var(--ln); }
  .run .fcard[open]>summary::before { content:"▾ "; }
  .run .fcard .fname { color:var(--fg,#ece0d4); font-weight:700; letter-spacing:.01em; }
  .run .fcard .fmeta { color:var(--m); font-size:11.5px; font-variant-numeric:tabular-nums; white-space:nowrap; }
  .run .fcard>.args-d, .run .fcard>.turn, .run .fcard>.content, .run .fcard>.note { margin-left:12px; margin-right:10px; }
  .run .fcard>.content { padding-bottom:8px; }
  .run .fcard>.turn:first-of-type { padding-top:8px; }
</style>
<div class="run">
  <div class="rmeta">
    <span class="chip">{{len .Tasks}} 個任務 · {{len .Turns}} 步</span>
    <span class="chip">${{printf "%.4f" .Meta.Cost}}</span>
    <span class="chip">tok in {{.Meta.PromptTokens}} · out {{.Meta.CompletionTokens}}</span>
    {{if .HasSubagent}}<span class="badge">subagent 協同</span>{{else}}<span class="solo">主 agent 獨立完成</span>{{end}}
    {{if .Meta.Running}}<span class="badge">執行中</span>{{end}}
    {{with .Meta.Model}}<span class="chip">{{.}}</span>{{end}}
    {{with .Meta.UpdatedAt}}<span class="chip">{{.}}</span>{{end}}
  </div>
  {{range .Tasks}}
  <details class="taskgrp"{{if .Open}} open{{end}}>
    <summary>{{trunc .Query}}<span class="tchips">{{.Steps}} 步{{if .CostUSD}} · ${{printf "%.4f" .CostUSD}}{{end}}</span></summary>
    <div class="q"><span class="lbl">收到任務</span>{{.Query}}</div>
    {{template "turns" .Turns}}
  </details>
  {{end}}
</div>
{{define "turns"}}
  {{range .}}
    {{if .Note}}
      <div class="note">[系統提醒] {{.Note}}</div>
    {{else if .FinalAnswer}}
      <div class="turn answer"><span class="tn">{{printf "%02d" .Index}}</span><span class="ans-title">最終回答</span><div class="content">{{.FinalAnswer}}</div>{{if .Usage}}<div class="usage">{{if .CostUSD}}<span class="cost">${{printf "%.4f" .CostUSD}}</span> · {{end}}in {{.Usage.PromptTokens}} tk（快取讀 {{.Usage.CacheReadTokens}}／寫 {{.Usage.CacheCreationTokens}}）· out {{.Usage.CompletionTokens}} tk</div>{{end}}</div>
    {{else}}
      <div class="turn"><span class="tn">{{printf "%02d" .Index}}</span>
        {{if .Thinking}}<details open class="think"><summary>思考</summary><div class="content">{{.Thinking}}</div></details>{{end}}
        {{range .Actions}}
            <div class="action"><span class="am">▸</span><b>{{.Tool}}</b>
              <pre class="args">{{.Args}}</pre>
              {{if .Observation}}<details><summary>觀察</summary><div class="content">{{.Observation}}</div></details>{{end}}
            </div>
        {{end}}
        {{if .Fan}}
        <div class="fan">
          <div class="fanroot">⤷ 委派 {{len .Fan}} 個子 agent{{if gt (len .Fan) 1}} · 並行{{end}}</div>
          <div class="fancards">
          {{range .Fan}}
            <details class="fcard">
              <summary><span class="fname">{{.AgentType}}</span><span class="fmeta">{{if .SubTurns}}{{.SubSteps}} 步{{if .SubCostUSD}} · <span class="cost">${{printf "%.4f" .SubCostUSD}}</span>{{end}}{{else}}報告{{end}}</span></summary>
              <details class="args-d"><summary>交辦</summary><pre class="args">{{.Args}}</pre></details>
              {{if .SubTurns}}{{template "turns" .SubTurns}}
              {{else if .Report}}<div class="content">{{.Report}}</div>{{end}}
            </details>
          {{end}}
          </div>
        </div>
        {{end}}
        {{if .Usage}}<div class="usage">{{if .CostUSD}}<span class="cost">${{printf "%.4f" .CostUSD}}</span> · {{end}}in {{.Usage.PromptTokens}} tk（快取讀 {{.Usage.CacheReadTokens}}／寫 {{.Usage.CacheCreationTokens}}）· out {{.Usage.CompletionTokens}} tk</div>{{end}}
      </div>
    {{end}}
  {{end}}
{{end}}`))
