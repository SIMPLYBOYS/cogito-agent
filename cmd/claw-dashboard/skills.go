package main

import (
	"bytes"
	"html/template"
	"net/http"
	"os"
	"sort"
)

// skillRow 是 UI 用：Dir 是資料夾名（編輯／刪除的識別），其餘是 SKILL.md 的內容。
type skillRow struct {
	Dir, Name, Desc, Body string
}

type skillsData struct {
	Skills []skillRow
	Flash  string
}

// readEditableSkills 列出 .claw/skills/ 底下【單層】的技能資料夾。
//
// 注意：SkillLoader 是遞迴走訪任意深度找 SKILL.md，這裡只列單層——因為單層是自生成與晉升
// 共同的慣例（<name>/SKILL.md），也是編輯表單能安全定位的結構。若有人手動放了巢狀技能，
// agent 讀得到但這裡不列，屬刻意的保守。
func readEditableSkills(workspace string) []skillRow {
	entries, err := os.ReadDir(activeSkillsDir(workspace))
	if err != nil {
		return nil
	}
	var out []skillRow
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name, desc, body, ok := readSkillSource(workspace, e.Name())
		if !ok {
			continue
		}
		if name == "" {
			name = e.Name()
		}
		out = append(out, skillRow{Dir: e.Name(), Name: name, Desc: desc, Body: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// skills 列出並可編輯 agent 生效中的技能。
//
// 【為何面板可寫、agent 不行】.claw/ 對【檔案工具】是唯讀（防 agent 自我晉升、繞過人工放行）；
// 面板的操作者是人，且綁 loopback + CSRF——與金鑰／MCP／cron 同一個信任模型。但人寫的技能
// 一樣要過 evolve 的安全閘：技能是「未來行為的來源」，不因作者是人就免驗。
func (s *server) skills(w http.ResponseWriter, r *http.Request) {
	d := skillsData{Skills: readEditableSkills(s.workspace), Flash: s.readFlash()}
	var b bytes.Buffer
	_ = skillsTmpl.Execute(&b, d)
	s.render(w, "Skills", template.HTML(b.String()))
}

func (s *server) skillSave(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	err := saveSkill(s.workspace, r.FormValue("dir"), r.FormValue("name"), r.FormValue("desc"), r.FormValue("body"))
	if err != nil {
		s.setFlash("⚠️ " + err.Error())
	} else {
		s.setFlash("✓ 已儲存技能——bot 下次組 prompt 時即生效（免重啟）。")
	}
	http.Redirect(w, r, "/skills", http.StatusSeeOther)
}

func (s *server) skillRemove(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	if err := removeSkill(s.workspace, r.FormValue("dir")); err != nil {
		s.setFlash("⚠️ " + err.Error())
	} else {
		s.setFlash("✓ 已移除技能。")
	}
	http.Redirect(w, r, "/skills", http.StatusSeeOther)
}

var skillsTmpl = template.Must(template.New("skills").Parse(`
{{if .Flash}}<p class="banner">{{.Flash}}</p>{{end}}
<p class="muted">agent 生效中的技能（<code>.claw/skills/&lt;資料夾&gt;/SKILL.md</code>）。
開局只注入<b>名稱＋描述</b>（漸進式暴露），正文由 agent 用 <code>read_skill</code> 按需載入
——所以描述要寫成「什麼情況該用這個技能」，那是模型唯一的挑選依據。</p>
<p class="muted">存檔會過與晉升相同的<b>安全把關</b>（結構＋危險指令掃描）。agent 自己反思出的技能仍走
<a href="/governance">governance</a> 晉升，不會直接出現在這裡。</p>

{{if .Skills}}
<div class="skills">
{{range .Skills}}
  <div class="skill">
    <div><b>{{.Name}}</b> <span class="muted">{{.Desc}}</span> <code>{{.Dir}}</code></div>
    <details><summary>正文</summary><pre class="skillbody">{{.Body}}</pre></details>
    <details class="mcpedit"><summary>編輯</summary>
      <form method="POST" action="/skills/save" class="knobs">
        <input type="hidden" name="dir" value="{{.Dir}}">
        <label>名稱 <input class="wide" name="name" value="{{.Name}}" required></label>
        <label>描述（什麼情況該用） <input class="wide" name="desc" value="{{.Desc}}" required></label>
        <label>正文 <textarea name="body" rows="10" required>{{.Body}}</textarea></label>
        <button>儲存</button>
      </form>
    </details>
    <details class="danger"><summary>移除</summary>
      <div class="confirm">
        <span>確定移除技能「<b>{{.Name}}</b>」？<code>.claw/skills/{{.Dir}}/</code> 整個刪除，無法復原。</span>
        <form method="POST" action="/skills/remove"><input type="hidden" name="dir" value="{{.Dir}}"><button type="submit" class="gact">確定移除</button></form>
      </div>
    </details>
  </div>
{{end}}
</div>
{{else}}<p class="muted">（尚無技能。用下方表單新增，或到 <a href="/governance">governance</a> 晉升 agent 反思出的提案。）</p>{{end}}

<details class="mcpedit"><summary>＋新增技能</summary>
  <form method="POST" action="/skills/save" class="knobs">
    <label>名稱 <input class="wide" name="name" placeholder="git-triage" required></label>
    <label>描述（什麼情況該用） <input class="wide" name="desc" placeholder="快速定位 regression：git bisect 二分 + 縮小重現" required></label>
    <label>正文 <textarea name="body" rows="10" placeholder="1. 先寫最小重現腳本 repro.sh&#10;2. git bisect start；標好 good/bad&#10;3. bisect run ./repro.sh 自動收斂到首個壞 commit" required></textarea></label>
    <button>新增</button>
  </form>
  <p class="fhint">資料夾名由名稱自動推出（小寫、非英數轉連字號），故名稱請含英數。正文至少 20 字。</p>
</details>
`))
