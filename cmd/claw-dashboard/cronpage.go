package main

import (
	"bytes"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"
)

// cronRow 是 UI 顯示用（job + 算好的下次觸發時間 + 回放連結）。
type cronRow struct {
	cronJob
	NextRun   string
	SessionID string
	Running   bool
}

type cronData struct {
	Jobs          []cronRow
	SchedulerOn   bool // chat（寫入）啟用＝排程器會真的觸發
	Flash         string
	NotifyTarget  string   // 原始字串（表單預填）
	NotifyTargets []string // 拆好的多目標（顯示用；空＝不推播）
	NotifyErrOnly bool
	TZName        string // 排程解讀用時區（顯示用）
	TZValue       string // CRON_TZ 現值（表單預填；空＝跟隨伺服器）
	TZWarn        string // 時區設定有問題時的提示
}

// cron 頁：列出排程任務，可新增/編輯/停用/移除/立即執行。CRUD 只是編 .claw/cron.json（安全），
// 【執行】需 COGITO_DASH_CHAT=1——cron 會驅動 agent（跑 bash/寫檔），沿用同一個寫入閘。
func (s *server) cronPage(w http.ResponseWriter, r *http.Request) {
	jobs, err := readCronJobs(cronConfigPath(s.workspace))
	if err != nil {
		http.Error(w, "讀 cron.json 失敗："+err.Error(), http.StatusInternalServerError)
		return
	}
	loc, tzWarn := cronLocation()
	now := time.Now().In(loc)
	d := cronData{
		SchedulerOn:   s.cron != nil,
		Flash:         s.readFlash(),
		NotifyTarget:  cronNotifyTarget(),
		NotifyTargets: splitNotifyTargets(cronNotifyTarget()),
		NotifyErrOnly: cronNotifyErrorsOnly(),
		TZName:        loc.String(),
		TZValue:       strings.TrimSpace(os.Getenv(cronTZKey)),
		TZWarn:        tzWarn,
	}
	runningID := ""
	if s.cron != nil {
		runningID = s.cron.runningID()
	}
	for _, j := range jobs {
		row := cronRow{cronJob: j, SessionID: cronSessionID(j.ID), NextRun: "—", Running: j.ID == runningID}
		if sched, e := cronParser.Parse(j.Schedule); e == nil {
			row.NextRun = sched.Next(now).Format("01-02 15:04")
		}
		d.Jobs = append(d.Jobs, row)
	}

	var b bytes.Buffer
	_ = cronTmpl.Execute(&b, d)
	s.render(w, "Cron", template.HTML(b.String()))
}

func (s *server) cronAdd(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	err := addCronJob(cronConfigPath(s.workspace), r.FormValue("name"), r.FormValue("schedule"), r.FormValue("prompt"))
	s.cronDone(w, r, err, "✓ 已新增排程任務。")
}

func (s *server) cronEdit(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	err := editCronJob(cronConfigPath(s.workspace), r.FormValue("id"), r.FormValue("name"), r.FormValue("schedule"), r.FormValue("prompt"))
	s.cronDone(w, r, err, "✓ 已更新排程任務。")
}

func (s *server) cronRemove(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	s.cronDone(w, r, removeCronJob(cronConfigPath(s.workspace), r.FormValue("id")), "✓ 已移除排程任務。")
}

func (s *server) cronToggle(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	s.cronDone(w, r, toggleCronJob(cronConfigPath(s.workspace), r.FormValue("id")), "✓ 已切換啟用狀態。")
}

// cronRunNow 不等排程、立刻跑一次（背景 goroutine——engine.Run 可能數十秒，HTTP 不能等）。
func (s *server) cronRunNow(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	if s.cron == nil {
		s.cronDone(w, r, nil, "⚠️ 排程器未啟用（需 COGITO_DASH_CHAT=1）——未執行。")
		return
	}
	id := r.FormValue("id")
	jobs, err := readCronJobs(cronConfigPath(s.workspace))
	if err != nil {
		s.cronDone(w, r, err, "")
		return
	}
	for _, j := range jobs {
		if j.ID == id {
			go s.cron.fire(j, time.Now())
			s.cronDone(w, r, nil, "▶ 已觸發「"+j.Name+"」——執行樹見 /runs/"+cronSessionID(id))
			return
		}
	}
	s.cronDone(w, r, nil, "⚠️ 找不到該任務。")
}

// cronDone 統一收尾：有錯就把錯誤放進 flash，否則放成功訊息，然後導回 /cron。
func (s *server) cronDone(w http.ResponseWriter, r *http.Request, err error, okMsg string) {
	if err != nil {
		s.setFlash("⚠️ " + err.Error())
	} else if okMsg != "" {
		s.setFlash(okMsg)
	}
	http.Redirect(w, r, "/cron", http.StatusSeeOther)
}

var cronTmpl = template.Must(template.New("cron").Parse(`
{{if .Flash}}<p class="banner">{{.Flash}}</p>{{end}}
{{if .TZWarn}}<p class="banner">⚠️ {{.TZWarn}}</p>{{end}}
<p class="muted">到點自動把任務交給 agent 執行。排程用標準 cron 運算式（分 時 日 月 週），
時區 <b>{{.TZName}}</b>{{if not .TZValue}}（跟隨伺服器——雲端多為 UTC，建議明設）{{end}}。
{{if .SchedulerOn}}排程器<b>執行中</b>——每 30 秒檢查一次。{{else}}<b class="warn">排程器未啟用</b>：設 <code>COGITO_DASH_CHAT=1</code> 重啟才會真的觸發（現在僅能編輯）。{{end}}
只在本面板跑著時觸發，非 24/7 常駐。</p>

{{if .Jobs}}
<div class="cronlist">
{{range .Jobs}}
  <div class="cronitem">
    <div class="cronhead">
      <b>{{.Name}}</b> <code>{{.Schedule}}</code>
      {{if .Running}}<span class="pill run">執行中</span>{{end}}
      {{if .Enabled}}<span class="pill ok">啟用</span>{{else}}<span class="pill">停用</span>{{end}}
      {{if eq .LastStatus "error"}}<span class="pill err">上次失敗</span>{{else if eq .LastStatus "ok"}}<span class="pill ok">上次成功</span>{{end}}
    </div>
    <dl class="kv">
      <dt>下次</dt><dd>{{.NextRun}}</dd>
      <dt>上次</dt><dd>{{if .LastRun}}{{.LastRun}}{{else}}（尚未執行）{{end}}</dd>
      {{if .LastError}}<dt>錯誤</dt><dd class="warn">{{.LastError}}</dd>{{end}}
      <dt>執行樹</dt><dd><a href="/runs/{{.SessionID}}">/runs/{{.SessionID}}</a></dd>
    </dl>
    <p class="cronprompt muted">{{.Prompt}}</p>
    <div class="acts">
      <form method="POST" action="/cron/run"><input type="hidden" name="id" value="{{.ID}}"><button type="submit" class="gact">▶ 立即執行</button></form>
      <form method="POST" action="/cron/toggle"><input type="hidden" name="id" value="{{.ID}}"><button type="submit" class="gact ghost">{{if .Enabled}}停用{{else}}啟用{{end}}</button></form>
      <form method="POST" action="/cron/remove"><input type="hidden" name="id" value="{{.ID}}"><button type="submit" class="gact ghost">移除</button></form>
    </div>
    <details class="mcpedit"><summary>編輯</summary>
      <form method="post" action="/cron/edit" class="knobs">
        <input type="hidden" name="id" value="{{.ID}}">
        <label>名稱 <input name="name" value="{{.Name}}" required></label>
        <label>排程 <input name="schedule" value="{{.Schedule}}" required></label>
        <label>任務描述 <textarea name="prompt" rows="3" required>{{.Prompt}}</textarea></label>
        <button>儲存</button>
      </form>
    </details>
  </div>
{{end}}
</div>
{{else}}<p class="muted">（尚無排程任務。）</p>{{end}}

<h3>排程設定 <span class="muted">{{if .TZValue}}{{.TZName}}{{else}}<span class="warn">{{.TZName}} ⚠️ 未明設時區</span>{{end}} · {{if .NotifyTargets}}推播 {{range $i, $t := .NotifyTargets}}{{if $i}}、{{end}}{{$t}}{{end}}{{if .NotifyErrOnly}}（只送失敗）{{end}}{{else}}未推播{{end}}</span></h3>
<p class="muted">時區決定排程怎麼解讀（留空＝跟隨伺服器，雲端多為 UTC）。
推播是執行完把「狀態＋回覆摘要＋執行樹連結」送到 Slack／Telegram，留空＝不推播；
token 走 <a href="/platform">platform</a> 的「金鑰／祕密」區。</p>
<details class="mcpedit"{{if not .TZValue}} open{{end}}><summary>編輯時區／推播</summary>
  <form method="POST" action="/env-config" class="knobs">
    <input type="hidden" name="_fields" value="CRON_TZ COGITO_CRON_NOTIFY COGITO_CRON_NOTIFY_ERRORS_ONLY">
    <input type="hidden" name="_return" value="/cron">
    <label>時區 <input name="CRON_TZ" value="{{.TZValue}}" placeholder="Asia/Taipei"></label>
    <p class="fhint">IANA 名稱。留空＝跟隨伺服器（雲端多為 UTC）。</p>

    <label>推播目標 <input class="wide" name="COGITO_CRON_NOTIFY" value="{{.NotifyTarget}}" placeholder="telegram:12345678, slack:C0123ABC"></label>
    <p class="fhint">收件對象的<b>頻道／聊天室 id</b>，不是 token。多個用逗號分隔，可同時送
    Telegram 與 Slack。token 設在 <a href="/platform">platform</a> 的「金鑰／祕密」區。</p>

    <label class="tog"><input type="checkbox" name="COGITO_CRON_NOTIFY_ERRORS_ONLY" value="1"{{if .NotifyErrOnly}} checked{{end}}> 只在失敗時推播</label>
    <button>儲存</button>
  </form>
</details>

<details class="mcpedit"><summary>＋新增排程任務</summary>
  <form method="post" action="/cron/add" class="knobs">
    <label>名稱 <input name="name" placeholder="每日巡檢" required></label>
    <label>排程 <input name="schedule" placeholder="0 9 * * 1-5" required></label>
    <label>任務描述（交給 agent 的指令） <textarea name="prompt" rows="3" placeholder="檢查 CI 是否有失敗的 job，有的話摘要原因" required></textarea></label>
    <button>新增</button>
  </form>
  <p class="muted">範例：<code>*/15 * * * *</code> 每 15 分 ·
  <code>0 9 * * 1-5</code> 週一到五 09:00 ·
  <code>0 */2 * * *</code> 每 2 小時 ·
  <code>30 8 1 * *</code> 每月 1 號 08:30</p>
</details>
`))
