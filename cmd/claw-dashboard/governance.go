package main

import (
	"bytes"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// governanceData 是 /governance 頁的唯讀資料：提案佇列（on-disk）+ 授權名單（env）。
// 待審批（pending approvals）是 bot 進程內的 in-memory 狀態，獨立的 dashboard 進程看不到——誠實標示，
// 不假裝（要看得到需先把審批狀態落地磁碟，屬 bot 端改動、延後）。
type governanceData struct {
	Workspace      string
	Skills         []proposedSkill
	MemoryProposed string // AGENTS.proposed.md 預覽（空＝無）
	ConfigProposed string // config.proposed.json 內容（空＝無）
	Allowed        []string
	Admins         []string
	Flash          string // 上次放行動作的結果（顯示一次）
}

type proposedSkill struct{ Dir, Name, Desc string } // Dir＝資料夾名（晉升用）；Name＝顯示名（frontmatter）

func (s *server) governance(w http.ResponseWriter, r *http.Request) {
	claw := filepath.Join(s.workspace, ".claw")
	d := governanceData{
		Workspace:      s.workspace,
		Skills:         readProposedSkills(filepath.Join(claw, evolve.ProposedSkillsDirName)),
		MemoryProposed: readPreview(filepath.Join(claw, evolve.ProposedMemoryFileName), 1200),
		ConfigProposed: readPreview(filepath.Join(claw, evolve.ProposedConfigFileName), 2000),
		Allowed:        parseCSVEnv("COGITO_ALLOWED_USERS"),
		Admins:         parseCSVEnv("COGITO_ADMIN_USERS"),
		Flash:          s.readFlash(),
	}
	var b bytes.Buffer
	_ = govTmpl.Execute(&b, d)
	s.render(w, "Governance", template.HTML(b.String()))
}

func (s *server) setFlash(msg string) { s.flash.Store(msg) }

func (s *server) readFlash() string {
	m, _ := s.flash.Load().(string)
	if m != "" {
		s.flash.Store("") // 顯示一次即清
	}
	return m
}

// governance 的放行動作皆【寫入】：CSRF 防護同 chat；dashboard 綁 loopback＝操作者在機器上＝等同
// admin（與 chat 的信任模型一致），故不另做 per-user admin 檢查。放行後 PRG 轉回 /governance，
// 提案消失即視覺確認，flash 補一句結果。各 apply 函式本身仍有把關（config clamp、skill Gate）。

func (s *server) govApplyConfig(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	changes, err := evolve.ApplyProposedConfig(s.workspace)
	switch {
	case err != nil:
		s.setFlash("⚠️ 套用調參失敗：" + err.Error())
	case len(changes) == 0:
		s.setFlash("無調參提案可套用。")
	default:
		s.setFlash("✓ 已套用調參：" + strings.Join(changes, "、"))
	}
	http.Redirect(w, r, "/governance", http.StatusSeeOther)
}

func (s *server) govApplyMemory(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	applied, err := evolve.ApplyProposedMemory(s.workspace)
	switch {
	case err != nil:
		s.setFlash("⚠️ 套用記憶失敗：" + err.Error())
	case applied == "":
		s.setFlash("無記憶提案可套用。")
	default:
		s.setFlash("✓ 已放行記憶：" + applied)
	}
	http.Redirect(w, r, "/governance", http.StatusSeeOther)
}

func (s *server) govDiscardMemory(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	had, err := evolve.DiscardProposedMemory(s.workspace)
	switch {
	case err != nil:
		s.setFlash("⚠️ 丟棄記憶失敗：" + err.Error())
	case !had:
		s.setFlash("無記憶提案可丟棄。")
	default:
		s.setFlash("✓ 已丟棄記憶提案。")
	}
	http.Redirect(w, r, "/governance", http.StatusSeeOther)
}

func (s *server) govPromoteSkill(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	// 防路徑穿越：只收單層資料夾名（filepath.Base 過濾掉任何 / 或 ..）。
	if name == "" || filepath.Base(name) != name {
		s.setFlash("⚠️ 無效的技能名。")
		http.Redirect(w, r, "/governance", http.StatusSeeOther)
		return
	}
	claw := filepath.Join(s.workspace, ".claw")
	proposedDir := filepath.Join(claw, evolve.ProposedSkillsDirName, name)
	if fi, err := os.Stat(proposedDir); err != nil || !fi.IsDir() {
		s.setFlash("⚠️ 找不到提案技能：" + name)
		http.Redirect(w, r, "/governance", http.StatusSeeOther)
		return
	}
	res, err := evolve.Promote(proposedDir, filepath.Join(claw, evolve.ActiveSkillsDirName))
	switch {
	case err != nil:
		s.setFlash("⚠️ 晉升失敗（" + name + "）：" + err.Error())
	case !res.Passed:
		s.setFlash("✗ 把關未過（" + name + "）：" + strings.Join(res.Issues, "；"))
	default:
		s.setFlash("✓ 已晉升技能：" + name)
	}
	http.Redirect(w, r, "/governance", http.StatusSeeOther)
}

// readProposedSkills 掃 skills-proposed/*/SKILL.md，取 frontmatter 的 name/description。
func readProposedSkills(dir string) []proposedSkill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []proposedSkill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		md, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		name, desc := scanFrontmatter(string(md))
		if name == "" {
			name = e.Name()
		}
		out = append(out, proposedSkill{Dir: e.Name(), Name: name, Desc: desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func scanFrontmatter(md string) (name, desc string) {
	seenOpen := false
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if line == "---" {
			if seenOpen {
				break // frontmatter 結束
			}
			seenOpen = true
			continue
		}
		switch {
		case strings.HasPrefix(line, "name:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		case strings.HasPrefix(line, "description:"):
			desc = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return
}

// readPreview 讀檔並截斷成預覽（缺檔回空）。
func readPreview(path string, max int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return schema.TruncRunes(strings.TrimSpace(string(b)), max, "\n…（截斷）")
}

func parseCSVEnv(key string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var govTmpl = template.Must(template.New("gov").Parse(`
<h2>提案佇列 <span class="muted">放行＝寫入；面板綁 loopback，操作者即 admin</span></h2>
{{with .Flash}}<div class="banner done">{{.}}</div>{{end}}
<p class="muted">workspace：<code>{{.Workspace}}</code></p>

<h3>技能提案 <span class="muted">skills-proposed/</span></h3>
{{if .Skills}}<ul class="gitems">{{range .Skills}}<li>
  <span><b>{{.Name}}</b>{{with .Desc}} — <span class="muted">{{.}}</span>{{end}}</span>
  <form method="POST" action="/governance/promote-skill"><input type="hidden" name="name" value="{{.Dir}}"><button type="submit" class="gact">晉升</button></form>
</li>{{end}}</ul>
<p class="muted">晉升會先過確定性把關（結構＋安全），通過才移到 <code>.claw/skills/</code> 生效。</p>
{{else}}<p class="muted">無。</p>{{end}}

<h3>記憶/慣例提案 <span class="muted">AGENTS.proposed.md</span></h3>
{{if .MemoryProposed}}<pre class="prev">{{.MemoryProposed}}</pre>
<div class="grow">
  <form method="POST" action="/governance/apply-memory"><button type="submit" class="gact">放行記憶</button></form>
  <form method="POST" action="/governance/discard-memory"><button type="submit" class="gact ghost">丟棄</button></form>
</div>
{{else}}<p class="muted">無。</p>{{end}}

<h3>調參提案 <span class="muted">config.proposed.json</span></h3>
{{if .ConfigProposed}}<pre class="prev">{{.ConfigProposed}}</pre>
<div class="grow">
  <form method="POST" action="/governance/apply-config"><button type="submit" class="gact">套用調參</button></form>
  <span class="hint">套用時一律 clamp 有界（越界夾回）。也可到 <a href="/platform">Platform</a> 直接編輯生效值。</span>
</div>
{{else}}<p class="muted">無。</p>{{end}}

<h2>授權名單 <span class="muted">env，只顯示 id</span></h2>
<dl class="kv">
  <dt>可驅動 agent（ALLOWED_USERS）</dt><dd>{{if .Allowed}}{{range .Allowed}}<code>{{.}}</code> {{end}}{{else}}<span class="muted">（未設＝fail-closed 拒絕所有）</span>{{end}}</dd>
  <dt>可審批（ADMIN_USERS）</dt><dd>{{if .Admins}}{{range .Admins}}<code>{{.}}</code> {{end}}{{else}}<span class="muted">（未設＝回退為 ALLOWED_USERS）</span>{{end}}</dd>
</dl>

<h2>待審批</h2>
<p class="muted">⚠️ 待審批的高危操作是 bot 進程內的即時狀態（in-memory），獨立的 dashboard 進程看不到。
要在面板看到需先把審批狀態落地磁碟（屬 bot 端改動，尚未做）。目前請在 chat 回覆 <code>approve</code>/<code>reject</code>。</p>
`))
