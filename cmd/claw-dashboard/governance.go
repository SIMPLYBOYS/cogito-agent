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
}

type proposedSkill struct{ Name, Desc string }

func (s *server) governance(w http.ResponseWriter, r *http.Request) {
	claw := filepath.Join(s.workspace, ".claw")
	d := governanceData{
		Workspace:      s.workspace,
		Skills:         readProposedSkills(filepath.Join(claw, evolve.ProposedSkillsDirName)),
		MemoryProposed: readPreview(filepath.Join(claw, evolve.ProposedMemoryFileName), 1200),
		ConfigProposed: readPreview(filepath.Join(claw, evolve.ProposedConfigFileName), 2000),
		Allowed:        parseCSVEnv("COGITO_ALLOWED_USERS"),
		Admins:         parseCSVEnv("COGITO_ADMIN_USERS"),
	}
	var b bytes.Buffer
	_ = govTmpl.Execute(&b, d)
	render(w, "Governance（檢視）", template.HTML(b.String()))
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
		out = append(out, proposedSkill{Name: name, Desc: desc})
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
<h2>提案佇列（放行動作走 chat / CLI，本面板只檢視）</h2>
<p class="muted">workspace：<code>{{.Workspace}}</code></p>

<h3>技能提案 <span class="muted">skills-proposed/</span></h3>
{{if .Skills}}<ul>{{range .Skills}}<li><b>{{.Name}}</b>{{with .Desc}} — <span class="muted">{{.}}</span>{{end}}</li>{{end}}</ul>
<p class="muted">放行：<code>go run ./cmd/skillgate -promote &lt;name&gt;</code>（把關通過才生效）</p>
{{else}}<p class="muted">無。</p>{{end}}

<h3>記憶/慣例提案 <span class="muted">AGENTS.proposed.md</span></h3>
{{if .MemoryProposed}}<pre class="prev">{{.MemoryProposed}}</pre>
<p class="muted">放行：在 chat 打 <code>apply memory</code>（僅 admin）</p>
{{else}}<p class="muted">無。</p>{{end}}

<h3>調參提案 <span class="muted">config.proposed.json</span></h3>
{{if .ConfigProposed}}<pre class="prev">{{.ConfigProposed}}</pre>
<p class="muted">放行：在 chat 打 <code>apply config</code>（僅 admin；套用時再 clamp 有界）</p>
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
