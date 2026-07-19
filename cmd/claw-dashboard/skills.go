package main

import (
	"html/template"
	"net/http"
	"strings"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

// skills 列出 agent 生效中的技能（.claw/skills/<name>/SKILL.md）。唯讀檢視——新增/晉升走 governance
// 的把關流程（結構＋安全），面板不直接寫技能檔。
func (s *server) skills(w http.ResponseWriter, r *http.Request) {
	list := ctxpkg.NewSkillLoader(s.workspace).List()

	var b strings.Builder
	b.WriteString(`<p class="muted">agent 生效中的技能（<code>.claw/skills/&lt;名稱&gt;/SKILL.md</code>）。` +
		`開局只注入名稱＋描述（漸進式暴露），正文由 agent 用 <code>read_skill</code> 按需載入。` +
		`新增／晉升技能走 <a href="/governance">governance</a> 的把關流程。</p>`)

	if len(list) == 0 {
		b.WriteString(`<p class="muted">（尚無技能。到 <a href="/governance">governance</a> 晉升提案技能，` +
			`或手動放 <code>.claw/skills/&lt;名稱&gt;/SKILL.md</code>。）</p>`)
		s.render(w, "Skills", template.HTML(b.String()))
		return
	}

	b.WriteString(`<div class="skills">`)
	for _, sk := range list {
		b.WriteString(`<details class="skill"><summary><b>` + template.HTMLEscapeString(sk.Name) +
			`</b> <span class="muted">` + template.HTMLEscapeString(sk.Description) + `</span></summary>`)
		b.WriteString(`<pre class="skillbody">` + template.HTMLEscapeString(sk.Body) + `</pre></details>`)
	}
	b.WriteString(`</div>`)
	s.render(w, "Skills", template.HTML(b.String()))
}
