package context

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentDef 是一個【具名子 agent】的定義（.claw/agents/<name>.md）：frontmatter 帶 name/description/tools，
// body 是該 agent 的 system prompt（角色與紀律）。讓 spawn_subagent 從「單一探路者」變成「一組專才」
// （code-reviewer / planner / security-auditor…），複用同一套 RunSub 隔離委派機制。
type AgentDef struct {
	Name        string
	Description string
	Tools       []string // 該 agent 可用的工具名（現有子 agent 工具的子集；空＝沿用預設探索工具集）
	Prompt      string   // system prompt（frontmatter 之後的正文）
}

// AgentLoader 從 .claw/agents/ 載入具名 agent 定義（對齊 SkillLoader / MemoryLoader 的漸進式載入）。
type AgentLoader struct {
	workDir string
}

func NewAgentLoader(workDir string) *AgentLoader { return &AgentLoader{workDir: workDir} }

func (l *AgentLoader) dir() string { return filepath.Join(l.workDir, ".claw", "agents") }

// Load 讀取並解析指定名稱的 agent 定義。防路徑穿越（只取檔名片段）；找不到回錯。
func (l *AgentLoader) Load(name string) (AgentDef, error) {
	safe := filepath.Base(strings.TrimSpace(name))
	data, err := os.ReadFile(filepath.Join(l.dir(), safe+".md"))
	if err != nil {
		return AgentDef{}, fmt.Errorf("找不到 agent 定義 %q（應為 .claw/agents/%s.md）", name, safe)
	}
	def := parseAgentMD(string(data))
	if def.Name == "" {
		def.Name = safe
	}
	return def, nil
}

// Index 列出可用的 agent（名稱 + 描述），放進 spawn_subagent 的說明，讓模型知道有哪些角色可派。
// 無 .claw/agents 或空目錄則回空字串（此時 spawn_subagent 沿用預設探路者，行為與過去一致）。
func (l *AgentLoader) Index() string {
	entries, err := os.ReadDir(l.dir())
	if err != nil {
		return ""
	}
	type row struct{ name, desc string }
	var rows []row
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(l.dir(), e.Name()))
		if rerr != nil {
			continue
		}
		d := parseAgentMD(string(data))
		name := d.Name
		if name == "" {
			name = strings.TrimSuffix(e.Name(), ".md")
		}
		rows = append(rows, row{name, d.Description})
	}
	if len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "  - %s：%s\n", r.name, r.desc)
	}
	return b.String()
}

// parseAgentMD 解析 agent 定義（frontmatter name/description/tools + body 為 system prompt）。
// 沿用技能/記憶的 frontmatter 格式。
func parseAgentMD(content string) AgentDef {
	def := AgentDef{Prompt: strings.TrimSpace(content)}
	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) == 3 {
			def.Prompt = strings.TrimSpace(parts[2])
			for _, line := range strings.Split(parts[1], "\n") {
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "name:"):
					def.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				case strings.HasPrefix(line, "description:"):
					def.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				case strings.HasPrefix(line, "tools:"):
					def.Tools = parseTags(strings.TrimPrefix(line, "tools:")) // 複用記憶的 list 解析
				}
			}
		}
	}
	return def
}
