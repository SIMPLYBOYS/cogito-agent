package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
)

// 技能檔的建立／編輯／刪除。
//
// 【為何面板可以寫、agent 不行】`.claw/` 是 agent 的控制面，檔案工具一律擋（見 tools/path.go
// 的 resolveForWrite）——擋的是「agent 自我晉升技能、繞過人工放行」。面板的操作者是【人】，
// 且面板綁 loopback、有 CSRF：與金鑰輪替／MCP／cron 同一個信任模型，沒道理技能反而不能改。
//
// 但人手寫的技能【一樣要過安全閘】：技能是「未來行為的來源」，一句 `curl … | sh` 寫進正文，
// agent 日後就會照做。故走與晉升相同的 evolve.GateContent，不因為是人寫的就放行。

// skillSlugRe 限制資料夾名為安全識別字（英數 + -_，≤64）——它是路徑片段，且 SkillLoader 靠它定位。
var skillSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// skillDirName 由技能名推出資料夾名：小寫、非英數轉連字號、收邊。與 evolve 的 slug 同規則
// （那個未匯出），保持人建與自生成的目錄慣例一致。
func skillDirName(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func activeSkillsDir(workspace string) string {
	return filepath.Join(workspace, ".claw", evolve.ActiveSkillsDirName)
}

// buildSkillMD 把三個欄位組成 SKILL.md 全文（frontmatter + 正文）。
func buildSkillMD(name, desc, body string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n",
		strings.TrimSpace(name), strings.TrimSpace(desc), strings.TrimSpace(body))
}

// saveSkill 建立或覆寫一個技能。dir 為空＝新增（由 name 推出資料夾名）；非空＝編輯既有技能。
//
// 【先驗後寫】組好內容立刻過 GateContent，不過就整個不寫——避免「先落檔再檢查」在磁碟上留下
// 一個未把關的技能檔，那段空窗期 agent 可能就載入了。
func saveSkill(workspace, dir, name, desc, body string) error {
	name, desc, body = strings.TrimSpace(name), strings.TrimSpace(desc), strings.TrimSpace(body)
	if name == "" {
		return fmt.Errorf("技能名不可為空")
	}

	if dir == "" {
		dir = skillDirName(name)
	}
	if !skillSlugRe.MatchString(dir) {
		return fmt.Errorf("無效的技能資料夾名 %q（只允許小寫英數與 -_，≤64 字；技能名請含英數）", dir)
	}

	content := buildSkillMD(name, desc, body)
	if res := evolve.GateContent(content); !res.Passed {
		return fmt.Errorf("未通過安全把關：%s", strings.Join(res.Issues, "；"))
	}

	skillDir := filepath.Join(activeSkillsDir(workspace), dir)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("建立技能目錄失敗: %w", err)
	}
	path := filepath.Join(skillDir, evolve.SkillFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("寫入技能失敗: %w", err)
	}
	return os.Rename(tmp, path) // 原子寫：避免 agent 讀到半截檔
}

// removeSkill 刪掉一個生效中的技能（整個資料夾）。
func removeSkill(workspace, dir string) error {
	if !skillSlugRe.MatchString(dir) {
		return fmt.Errorf("無效的技能資料夾名 %q", dir)
	}
	// 再保險一次：解出的路徑必須仍在 skills/ 底下（防任何形式的穿越）。
	base := activeSkillsDir(workspace)
	target := filepath.Join(base, dir)
	if rel, err := filepath.Rel(base, target); err != nil || rel != dir {
		return fmt.Errorf("拒絕刪除工作區外的路徑")
	}
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return fmt.Errorf("找不到技能 %q", dir)
	}
	return os.RemoveAll(target)
}

// readSkillSource 讀回某技能的原始 SKILL.md（供編輯表單預填）。
func readSkillSource(workspace, dir string) (name, desc, body string, ok bool) {
	if !skillSlugRe.MatchString(dir) {
		return "", "", "", false
	}
	data, err := os.ReadFile(filepath.Join(activeSkillsDir(workspace), dir, evolve.SkillFileName))
	if err != nil {
		return "", "", "", false
	}
	n, d, b := splitSkillMD(string(data))
	return n, d, b, true
}

// splitSkillMD 從 SKILL.md 拆出 name / description / 正文（frontmatter 解析與 evolve 一致）。
func splitSkillMD(content string) (name, desc, body string) {
	body = content
	if !strings.HasPrefix(content, "---") {
		return "", "", strings.TrimSpace(body)
	}
	parts := strings.SplitN(content, "---", 3)
	if len(parts) != 3 {
		return "", "", strings.TrimSpace(body)
	}
	body = strings.TrimSpace(parts[2])
	for _, line := range strings.Split(parts[1], "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "name:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		case strings.HasPrefix(line, "description:"):
			desc = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return name, desc, body
}
