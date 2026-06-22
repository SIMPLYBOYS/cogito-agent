package context

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Skill struct {
	Name        string
	Description string
	Body        string
}

type SkillLoader struct {
	workDir string
}

func NewSkillLoader(workDir string) *SkillLoader {
	return &SkillLoader{workDir: workDir}
}

// loadAll 走訪 .claw/skills，解析所有 SKILL.md 為 Skill 列表（含正文）。
func (s *SkillLoader) loadAll() []Skill {
	base := filepath.Join(s.workDir, ".claw", "skills")
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil
	}
	var skills []Skill
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "SKILL.md" {
			if content, e := os.ReadFile(path); e == nil {
				skills = append(skills, parseSkillMD(string(content)))
			}
		}
		return nil
	})
	return skills
}

// LoadIndex 只把技能的【元數據】（名稱 + 觸發描述）放進 System Prompt（漸進式暴露）；
// 正文不在此載入，避免技能多時開局就吃掉大量 token。模型需要時用 read_skill 工具按需載入。
func (s *SkillLoader) LoadIndex() string {
	skills := s.loadAll()
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n### 可用專業技能索引 (Agent Skills)\n")
	b.WriteString("以下是你擁有的技能【索引】（僅名稱與適用場景）。當任務符合某技能的描述時，依其正文指令執行。載入正文有兩種方式：\n")
	b.WriteString("1. **`read_skill`**：把技能正文載入【你自己的 context】，適合你要親自一步步操作的情境。\n")
	b.WriteString("2. **`spawn_subagent` 的 `skill` 參數**：把技能正文只載入【子智能體的隔離 context】，由它執行完只回傳結論——適合長篇操作指南或你想保持主 context 精簡時。\n\n")
	for _, sk := range skills {
		b.WriteString(fmt.Sprintf("- **%s**：%s\n", sk.Name, sk.Description))
	}
	return b.String()
}

// ReadSkill 返回指定技能的完整正文（供 read_skill 工具按需載入）。
func (s *SkillLoader) ReadSkill(name string) (string, error) {
	for _, sk := range s.loadAll() {
		if sk.Name == name {
			return sk.Body, nil
		}
	}
	return "", fmt.Errorf("找不到名為 %q 的技能（請對照技能索引中的名稱）", name)
}

func parseSkillMD(content string) Skill {
	skill := Skill{
		Name:        "Unknown Skill",
		Description: "No description provided.",
		Body:        content,
	}

	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) == 3 {
			frontmatter := parts[1]
			skill.Body = strings.TrimSpace(parts[2])

			lines := strings.Split(frontmatter, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					skill.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				} else if strings.HasPrefix(line, "description:") {
					skill.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				}
			}
		}
	}

	return skill
}
