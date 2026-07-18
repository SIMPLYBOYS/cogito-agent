package replay

import (
	"bytes"
	"html/template"
)

// Fragment 把 Run 渲染成一段自包含的 HTML（含自己的 <style>，不依賴外部 CSS——這樣同一個渲染核心
// 未來也能包成獨立的靜態 artifact）。用 native <details> 折疊，零 JS。所有動態文字經 html/template
// 自動跳脫（來自工具輸出/模型文字，視為不受信）。
func Fragment(run Run) template.HTML {
	var b bytes.Buffer
	if err := fragmentTmpl.Execute(&b, run); err != nil {
		return template.HTML("<p>（run 渲染失敗）</p>")
	}
	return template.HTML(b.String())
}

var fragmentTmpl = template.Must(template.New("run").Parse(`<style>
  .run { --rl:#e8734a; }
  .run .q { border-left:3px solid var(--rl); padding:8px 12px; margin-bottom:14px; }
  .run .q .lbl { color:var(--mut,#888); font-size:12px; }
  .run .rmeta { color:var(--mut,#888); font-size:13px; margin-bottom:16px; display:flex; flex-wrap:wrap; gap:14px; }
  .run .rmeta .badge { color:var(--rl); border:1px solid var(--rl); border-radius:4px; padding:0 6px; }
  .run .turn { border-left:2px solid var(--line,#ddd); padding:2px 0 10px 14px; margin-left:6px; position:relative; }
  .run .turn .tn { position:absolute; left:-13px; top:0; background:var(--bg,#fff); color:var(--mut,#888); font-size:11px; }
  .run .turn.answer { border-left-color:var(--rl); }
  .run .action { margin:6px 0; }
  .run .action.sub { border-left:2px solid var(--rl); padding-left:10px; margin-left:2px; }
  .run .action .badge { color:var(--rl); font-size:12px; border:1px solid var(--rl); border-radius:4px; padding:0 5px; }
  .run details { margin:3px 0; } .run summary { cursor:pointer; color:var(--mut,#888); font-size:13px; }
  .run .content { white-space:pre-wrap; word-break:break-word; margin:4px 0 4px 10px; font-size:13.5px; }
  .run pre { white-space:pre-wrap; word-break:break-word; margin:4px 0 4px 10px; font-size:12.5px; color:var(--mut,#888); }
  .run .usage { color:var(--mut,#888); font-size:11px; margin-top:4px; }
  .run .note { color:var(--mut,#888); font-size:12.5px; font-style:italic; margin:6px 0; }
  .run .ans-title { color:var(--rl); }
  .run .subinner { margin:4px 0 4px 8px; border-left:1px dashed var(--rl); padding-left:8px; }
</style>
<div class="run">
  <div class="q"><div class="lbl">收到任務</div>{{.Query}}</div>
  <div class="rmeta">
    <span>{{len .Turns}} 步</span>
    <span>${{printf "%.4f" .Meta.Cost}}</span>
    <span>tok in {{.Meta.PromptTokens}} / out {{.Meta.CompletionTokens}}</span>
    {{if .HasSubagent}}<span class="badge">有 subagent 協同</span>{{else}}<span>主 agent 獨立完成</span>{{end}}
    {{if .Meta.Running}}<span class="badge">執行中</span>{{end}}
    {{with .Meta.Model}}<span>model: {{.}}</span>{{end}}
    {{with .Meta.UpdatedAt}}<span>{{.}}</span>{{end}}
  </div>
  {{template "turns" .Turns}}
</div>
{{define "turns"}}
  {{range .}}
    {{if .Note}}
      <div class="note">[系統提醒] {{.Note}}</div>
    {{else if .FinalAnswer}}
      <div class="turn answer"><span class="tn">#{{.Index}}</span><b class="ans-title">最終回答</b><div class="content">{{.FinalAnswer}}</div></div>
    {{else}}
      <div class="turn"><span class="tn">#{{.Index}}</span>
        {{if .Thinking}}<details open class="thinking"><summary>💭 思考</summary><div class="content">{{.Thinking}}</div></details>{{end}}
        {{range .Actions}}
          {{if .IsSubagent}}
            <div class="action sub">🤖 spawn_subagent <span class="badge">{{.AgentType}}</span>
              <details><summary>參數</summary><pre>{{.Args}}</pre></details>
              {{if .SubTurns}}<details class="subinner"><summary>🔬 子 agent 內部（{{len .SubTurns}} 步）</summary>{{template "turns" .SubTurns}}</details>
              {{else if .Report}}<details class="report"><summary>📋 子 agent 報告</summary><div class="content">{{.Report}}</div></details>{{end}}
            </div>
          {{else}}
            <div class="action">🔧 <b>{{.Tool}}</b> <pre>{{.Args}}</pre>
              {{if .Observation}}<details><summary>觀察</summary><div class="content">{{.Observation}}</div></details>{{end}}
            </div>
          {{end}}
        {{end}}
        {{if .Usage}}<div class="usage">tok in {{.Usage.PromptTokens}} / out {{.Usage.CompletionTokens}}</div>{{end}}
      </div>
    {{end}}
  {{end}}
{{end}}`))
