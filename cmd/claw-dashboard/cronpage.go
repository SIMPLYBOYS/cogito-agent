package main

import (
	"bytes"
	"html/template"
	"net/http"
	"time"
)

// cronRow 是 UI 顯示用（job + 算好的下次觸發時間 + 回放連結）。
type cronRow struct {
	cronJob
	NextRun   string
	SessionID string
}

type cronData struct {
	Jobs        []cronRow
	SchedulerOn bool // chat（寫入）啟用＝排程器會真的觸發
	Flash       string
}

// cron 頁：列出排程任務，可新增/編輯/停用/移除/立即執行。CRUD 只是編 .claw/cron.json（安全），
// 【執行】需 COGITO_DASH_CHAT=1——cron 會驅動 agent（跑 bash/寫檔），沿用同一個寫入閘。
func (s *server) cronPage(w http.ResponseWriter, r *http.Request) {
	jobs, err := readCronJobs(cronConfigPath(s.workspace))
	if err != nil {
		http.Error(w, "讀 cron.json 失敗："+err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	d := cronData{SchedulerOn: s.cron != nil, Flash: s.readFlash()}
	for _, j := range jobs {
		row := cronRow{cronJob: j, SessionID: cronSessionID(j.ID), NextRun: "—"}
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
<p class="muted">到點自動把任務交給 agent 執行。排程用標準 cron 運算式（分 時 日 月 週）。
{{if .SchedulerOn}}排程器<b>執行中</b>——每 30 秒檢查一次。{{else}}<b class="warn">排程器未啟用</b>：設 <code>COGITO_DASH_CHAT=1</code> 重啟才會真的觸發（現在僅能編輯）。{{end}}
只在本面板跑著時觸發，非 24/7 常駐。</p>

{{if .Jobs}}
<div class="cronlist">
{{range .Jobs}}
  <div class="cronitem">
    <div class="cronhead">
      <b>{{.Name}}</b> <code>{{.Schedule}}</code>
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
