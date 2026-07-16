// Package evolve 是 Tier 4「自我進化」的落點。第一塊：技能自生成（SkillSynthesizer）。
//
// 【安全鐵律】自我進化會改寫 Agent 未來行為的來源（技能/AGENTS.md/prompt），直接牴觸本專案的
// 「失控控制」主題。因此所有自生成產物一律寫進【暫存區】，絕不自動啟用——SkillLoader 只讀
// .claw/skills/，而本套件只寫 .claw/skills-proposed/，必須人工 review 後手動移過去才生效。
package evolve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// ProposedSkillsDirName 是提案技能的暫存子目錄（相對於 assets/workspace 根）。
const ProposedSkillsDirName = "skills-proposed"

// SkillFileName 是每個技能資料夾內的必備檔（對齊 agentskills.io 開放標準：folder-per-skill）。
// SkillLoader 也只認這個檔名，故自生成必須寫成 <name>/SKILL.md 才會被載入。
const SkillFileName = "SKILL.md"

// SkillSynthesizer 對一段已完成的任務軌跡反思，若存在可複用流程則寫成「提案技能」。
type SkillSynthesizer struct {
	provider    provider.LLMProvider
	proposedDir string // 提案技能寫入目錄（通常 <root>/.claw/skills-proposed）
}

func NewSkillSynthesizer(p provider.LLMProvider, proposedDir string) *SkillSynthesizer {
	return &SkillSynthesizer{provider: p, proposedDir: proposedDir}
}

// reflection 是反思的結構化輸出（要求模型只吐這個 JSON）。
type reflection struct {
	WorthSaving bool   `json:"worth_saving"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

const reflectSystemPrompt = `你是一個負責「技能萃取」的反思者。看完一段【已成功完成】的任務軌跡後，
判斷其中是否存在一個「可複用、可泛化」的操作流程，值得寫成技能（SKILL.md）供未來類似任務直接調用。

判準（從嚴）：
- 只有當流程【具體、可重複、跨任務有價值】時才保存；一次性瑣事、與本任務資料強綁定的步驟不要保存。
- 技能正文寫「怎麼做」的步驟指南，不要把這次的具體檔名/數值寫死。

body 請依 agentskills.io 慣例分三段（markdown）：
## When to use
- 觸發情境（什麼任務該用這技能）
## Steps
1. 具體、命令層級的步驟（不要寫死本次的檔名/數值）
## Examples
- 真實範例：命令 + 預期輸出

輸出規則：只輸出一個 JSON 物件，不要任何其他文字或 markdown 圍欄。
- 值得保存：{"worth_saving": true, "name": "<kebab-case 短名，限 a-z0-9._->", "description": "<一句話：做什麼 + 何時用>", "body": "<上述三段式 markdown>"}
- 不值得：{"worth_saving": false}`

// Reflect 反思一段軌跡。回傳寫出的提案技能檔路徑；空字串表示判定不值得保存（非錯誤）。
func (s *SkillSynthesizer) Reflect(ctx context.Context, taskPrompt string, history []schema.Message) (string, error) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: reflectSystemPrompt},
		{Role: schema.RoleUser, Content: fmt.Sprintf("任務：\n%s\n\n軌跡：\n%s", taskPrompt, renderTranscript(history, 6000))},
	}

	resp, err := s.provider.Generate(ctx, msgs, nil)
	if err != nil {
		return "", fmt.Errorf("反思 LLM 調用失敗: %w", err)
	}

	var r reflection
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &r); err != nil {
		return "", fmt.Errorf("反思輸出非合法 JSON（%q）: %w", resp.Content, err)
	}
	if !r.WorthSaving {
		return "", nil
	}
	if r.Name == "" || r.Body == "" {
		return "", fmt.Errorf("反思判定值得保存，但缺 name/body")
	}

	return s.writeProposed(r, taskPrompt)
}

// writeProposed 以 agentskills.io 的 folder-per-skill 結構寫進【暫存區】：<proposedDir>/<slug>/SKILL.md。
// 這也是 SkillLoader 唯一認得的結構（它只載入名為 SKILL.md 的檔），故晉升後才真的會被 agent 用到。
func (s *SkillSynthesizer) writeProposed(r reflection, taskPrompt string) (string, error) {
	ctxpkg.LockKnowledge() // 只鎖檔案寫尾段（反思的 LLM 呼叫在外層、不持鎖）
	defer ctxpkg.UnlockKnowledge()
	skillDir := filepath.Join(s.proposedDir, slug(r.Name))
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return "", fmt.Errorf("建立提案技能目錄失敗: %w", err)
	}
	path := filepath.Join(skillDir, SkillFileName)

	var b strings.Builder
	// 標準 frontmatter（name/description + 可選 version；溯源放 metadata）。
	b.WriteString("---\n")
	b.WriteString("name: " + slug(r.Name) + "\n")
	b.WriteString("description: " + oneLine(r.Description) + "\n")
	b.WriteString("version: 1\n")
	fmt.Fprintf(&b, "generated_by: cogito-agent\ngenerated_at: %s\n", time.Now().Format(time.RFC3339))
	b.WriteString("---\n")
	fmt.Fprintf(&b, "<!-- ⚠️ 自動生成的提案技能，需人工 review 後用 skillgate 晉升到 .claw/skills/ 才會生效。原任務：%s -->\n\n",
		oneLine(taskPrompt))
	b.WriteString(r.Body)
	if !strings.HasSuffix(r.Body, "\n") {
		b.WriteString("\n")
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("寫入提案技能失敗: %w", err)
	}
	return path, nil
}

// renderTranscript 把對話歷史壓成精簡軌跡文字（上限 maxChars，超過截斷尾部保留開頭）。
func renderTranscript(history []schema.Message, maxChars int) string {
	var b strings.Builder
	for _, m := range history {
		if m.Role == schema.RoleSystem {
			continue
		}
		line := string(m.Role) + ": " + oneLine(m.Content)
		for _, tc := range m.ToolCalls {
			line += fmt.Sprintf(" [呼叫工具 %s %s]", tc.Name, oneLine(string(tc.Arguments)))
		}
		b.WriteString(line + "\n")
	}
	s := b.String()
	if len(s) > maxChars {
		return s[:maxChars] + "\n...[軌跡過長已截斷]"
	}
	return s
}

func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return strings.TrimSpace(s)
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
	s = strings.TrimSpace(s)
	// 按字元切：這會寫進技能檔的 YAML frontmatter（description:），中文被 byte 切成非法
	// UTF-8 會直接汙染產出的技能檔。
	return schema.TruncRunes(s, 200, "…")
}

func slug(name string) string {
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
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "proposed-skill"
	}
	return out
}
