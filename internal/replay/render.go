package replay

import (
	"bytes"
	"html/template"
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

var fragmentTmpl = template.Must(template.New("run").Parse(`<style>
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
  .run .usage { color:var(--m); font-size:10.5px; margin-top:7px; letter-spacing:.03em; }
  .run .note { color:var(--m); font-size:12.5px; font-style:italic; margin:7px 0; opacity:.85; }
  /* 巢狀鍛爐：子 agent 內部 */
  .run .action.sub .badge { color:var(--a); border:1px solid var(--a); border-radius:5px; padding:0 7px; font-size:11px; margin-left:4px; }
  .run .subinner { margin:8px 0 2px 2px; background:color-mix(in srgb,var(--a2) 5%,transparent); border:1px solid var(--ln); border-left:2px solid var(--a2); border-radius:0 7px 7px 0; padding:2px 12px 8px; }
  .run .subinner>summary { color:var(--a2); font-size:12.5px; padding:6px 0 2px; }
</style>
<div class="run">
  <div class="q"><span class="lbl">收到任務</span>{{.Query}}</div>
  <div class="rmeta">
    <span class="chip">{{len .Turns}} 步</span>
    <span class="chip">${{printf "%.4f" .Meta.Cost}}</span>
    <span class="chip">tok in {{.Meta.PromptTokens}} · out {{.Meta.CompletionTokens}}</span>
    {{if .HasSubagent}}<span class="badge">subagent 協同</span>{{else}}<span class="solo">主 agent 獨立完成</span>{{end}}
    {{if .Meta.Running}}<span class="badge">執行中</span>{{end}}
    {{with .Meta.Model}}<span class="chip">{{.}}</span>{{end}}
    {{with .Meta.UpdatedAt}}<span class="chip">{{.}}</span>{{end}}
  </div>
  {{template "turns" .Turns}}
</div>
{{define "turns"}}
  {{range .}}
    {{if .Note}}
      <div class="note">[系統提醒] {{.Note}}</div>
    {{else if .FinalAnswer}}
      <div class="turn answer"><span class="tn">{{printf "%02d" .Index}}</span><span class="ans-title">最終回答</span><div class="content">{{.FinalAnswer}}</div></div>
    {{else}}
      <div class="turn"><span class="tn">{{printf "%02d" .Index}}</span>
        {{if .Thinking}}<details open class="think"><summary>思考</summary><div class="content">{{.Thinking}}</div></details>{{end}}
        {{range .Actions}}
          {{if .IsSubagent}}
            <div class="action sub"><span class="am">⤷</span><b>委派子 agent</b><span class="badge">{{.AgentType}}</span>
              <details><summary>參數</summary><pre class="args">{{.Args}}</pre></details>
              {{if .SubTurns}}<details class="subinner"><summary>子 agent 內部（{{len .SubTurns}} 步）</summary>{{template "turns" .SubTurns}}</details>
              {{else if .Report}}<details class="report"><summary>子 agent 報告</summary><div class="content">{{.Report}}</div></details>{{end}}
            </div>
          {{else}}
            <div class="action"><span class="am">▸</span><b>{{.Tool}}</b>
              <pre class="args">{{.Args}}</pre>
              {{if .Observation}}<details><summary>觀察</summary><div class="content">{{.Observation}}</div></details>{{end}}
            </div>
          {{end}}
        {{end}}
        {{if .Usage}}<div class="usage">tok in {{.Usage.PromptTokens}} · out {{.Usage.CompletionTokens}}</div>{{end}}
      </div>
    {{end}}
  {{end}}
{{end}}`))
