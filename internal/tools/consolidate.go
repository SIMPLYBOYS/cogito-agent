package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// ConsolidateTool 讓 agent【主動】把目前這段工作沉澱成長期資產：反思軌跡 → 提案技能/記憶/KG 關係。
// 與 post-task hook 互補——hook 由 harness 保證每次任務後觸發（可靠），這個工具讓 agent 在判斷「剛完成
// 一段有可複用價值的工作」時自己觸發（自主）。產物一律只進【提案】，須 review/gate 才生效，不繞過控制。
//
// 跑哪幾種反思，沿用 post-task hook 的同一組 opt-in 旗標（COGITO_SKILL_SYNTH / _MEMORY_SYNTH / _KG_SYNTH）。
type ConsolidateTool struct {
	provider provider.LLMProvider
	root     string
	session  *ctxpkg.Session
}

func NewConsolidateTool(p provider.LLMProvider, root string, session *ctxpkg.Session) *ConsolidateTool {
	return &ConsolidateTool{provider: p, root: root, session: session}
}

func (t *ConsolidateTool) Name() string { return "consolidate" }

func (t *ConsolidateTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "把目前這段工作主動沉澱成長期資產：反思軌跡，萃取可複用技能、耐久慣例/教訓、記憶間的關係（皆為提案，須人工 review/gate 才生效）。當你剛完成一個有可複用價值的子任務、或學到值得記住的慣例時調用——平時 harness 也會在任務結束自動做，這個工具讓你在當下覺得有價值時提前沉澱。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"note": map[string]any{
					"type":        "string",
					"description": "（可選）一句話描述這次工作的主題，幫助反思聚焦；省略則用對話起點推斷。",
				},
			},
		},
	}
}

func (t *ConsolidateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Note string `json:"note"`
	}
	_ = json.Unmarshal(args, &in)

	history := t.session.GetWorkingMemory(0)
	taskPrompt := strings.TrimSpace(in.Note)
	if taskPrompt == "" {
		taskPrompt = firstUserMessage(history)
	}
	if taskPrompt == "" {
		taskPrompt = "（本次工作）"
	}

	var parts []string
	if os.Getenv("COGITO_SKILL_SYNTH") == "1" {
		dir := filepath.Join(t.root, ".claw", evolve.ProposedSkillsDirName)
		if path, err := evolve.NewSkillSynthesizer(t.provider, dir).Reflect(ctx, taskPrompt, history); err == nil && path != "" {
			parts = append(parts, "1 個提案技能")
		}
	}
	if os.Getenv("COGITO_MEMORY_SYNTH") == "1" {
		if added, err := evolve.NewMemorySynthesizer(t.provider, t.root).Reflect(ctx, taskPrompt, history); err == nil && len(added) > 0 {
			parts = append(parts, fmt.Sprintf("%d 條提案記憶", len(added)))
		}
	}
	if os.Getenv("COGITO_KG_SYNTH") == "1" {
		if n, err := evolve.NewRelationExtractor(t.provider, t.root).Extract(ctx); err == nil && n > 0 {
			parts = append(parts, fmt.Sprintf("%d 條提案關係", n))
		}
	}

	if len(parts) == 0 {
		return "已嘗試沉澱，但本次沒有萃取到值得保存的新內容（或自我進化未啟用）。", nil
	}
	return "已沉澱：" + strings.Join(parts, "、") + "（皆為提案，須 review/gate 才生效，不會自動套用）。", nil
}

// firstUserMessage 取對話中第一條真正的使用者訊息（略過系統佔位符與 tool_result）。
func firstUserMessage(history []schema.Message) string {
	for _, m := range history {
		if m.Role == schema.RoleUser && m.ToolCallID == "" && !strings.HasPrefix(m.Content, "[系統佔位符]") {
			return m.Content
		}
	}
	return ""
}
