package tools

import (
	"context"
	"encoding/json"
	"fmt"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// ReadSkillTool 是漸進式技能載入（Progressive Disclosure）的按需載入端：System Prompt 只放
// 技能索引（名稱+描述），模型判定需要某技能時調用本工具載入其完整正文，避免開局就吃光 token。
type ReadSkillTool struct {
	loader *ctxpkg.SkillLoader
}

// NewReadSkillTool 的 skillsBaseDir 是含 .claw/skills 的目錄（須與 composer 取索引的目錄一致）。
func NewReadSkillTool(skillsBaseDir string) *ReadSkillTool {
	return &ReadSkillTool{loader: ctxpkg.NewSkillLoader(skillsBaseDir)}
}

func (t *ReadSkillTool) Name() string { return "read_skill" }

func (t *ReadSkillTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "按名稱載入一個專業技能的完整正文（操作指南）。當任務符合 System Prompt 中『技能索引』的某條描述時，先調用此工具取得正文再執行。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "技能名稱，須與技能索引中列出的名稱一致",
				},
			},
			"required": []string{"name"},
		},
	}
}

type readSkillArgs struct {
	Name string `json:"name"`
}

func (t *ReadSkillTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input readSkillArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}
	return t.loader.ReadSkill(input.Name)
}
