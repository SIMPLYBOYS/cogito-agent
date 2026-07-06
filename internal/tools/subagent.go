package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// AgentRunner 定義引擎向工具層暴露的"拉起子智能體"能力。接口定義在 tools 包（使用端），
// 這樣 tools 不需要 import engine，避免循環依賴；*engine.AgentEngine 靠 duck typing 滿足它。
// skillBody 為可選的「綁定技能正文」：非空時注入子 agent 的隔離 context，主 context 不被汙染。
type AgentRunner interface {
	RunSub(ctx context.Context, taskPrompt string, skillBody string, systemPrompt string, readOnlyRegistry Registry, reporter interface{}) (string, error)
}

// SubagentTool 是一個標準 BaseTool：主 agent 調用它來派出一個受限的探索子 agent，
// 子 agent 在隔離的上下文裡跑完，只回傳一段精煉報告——主 agent 的 session 不被搜索過程汙染。
// 可選地綁定一個技能（skill 參數）：該技能的完整正文只會載入子 agent 的隔離 context。
type SubagentTool struct {
	runner           AgentRunner
	readOnlyRegistry Registry
	reporter         interface{} // 暫用 interface 規避包依賴；RunSub 內部斷言回 engine.Reporter
	skillLoader      *ctxpkg.SkillLoader
	agentLoader      *ctxpkg.AgentLoader
}

// skillsBaseDir 是含 .claw/skills 與 .claw/agents 的目錄（須與主 agent 的索引同源）。
func NewSubagentTool(runner AgentRunner, readOnlyRegistry Registry, reporter interface{}, skillsBaseDir string) *SubagentTool {
	return &SubagentTool{
		runner:           runner,
		readOnlyRegistry: readOnlyRegistry,
		reporter:         reporter,
		skillLoader:      ctxpkg.NewSkillLoader(skillsBaseDir),
		agentLoader:      ctxpkg.NewAgentLoader(skillsBaseDir),
	}
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Definition() schema.ToolDefinition {
	desc := "派出一個子智能體在隔離 context 中執行子任務（探索/審查/規劃…），完畢後回傳一份精煉報告——主 context 不被過程汙染，可一次吐多個並行委派。"
	if idx := t.agentLoader.Index(); idx != "" {
		desc += "\n可用的 agent_type（不指定則為預設探路者，唯讀探索）：\n" + idx
	}
	desc += "可選 skill 參數：綁定技能後其完整正文只載入子 context，適合需要長篇操作指南的任務。"
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_prompt": map[string]interface{}{
					"type":        "string",
					"description": "給子智能體下達的明確任務指令。",
				},
				"agent_type": map[string]interface{}{
					"type":        "string",
					"description": "（可選）要派出的具名 agent（見本說明的清單）。指定後用該 agent 的角色 prompt 與工具集；不指定＝預設探路者。",
				},
				"skill": map[string]interface{}{
					"type":        "string",
					"description": "（可選）要綁定給子智能體的技能名稱，須與 System Prompt『技能索引』中的名稱一致。指定後該技能正文只進子 context。",
				},
			},
			"required": []string{"task_prompt"},
		},
	}
}

type subagentArgs struct {
	TaskPrompt string `json:"task_prompt"`
	AgentType  string `json:"agent_type"`
	Skill      string `json:"skill"`
}

func (t *SubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input subagentArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析參數失敗: %w", err)
	}

	// 具名 agent：載入其角色 prompt 與工具集（工具集是既有子 agent 工具的子集）。失敗則
	// error-as-observation，由主 agent 改用其他名稱或不指定。
	reg := t.readOnlyRegistry
	var systemPrompt string
	role := "探路者"
	if input.AgentType != "" {
		def, err := t.agentLoader.Load(input.AgentType)
		if err != nil {
			return fmt.Errorf("載入 agent 失敗: %v", err).Error(), nil
		}
		systemPrompt = def.Prompt
		role = def.Name
		if len(def.Tools) > 0 {
			reg = t.readOnlyRegistry.Subset(def.Tools) // 限縮到該 agent 宣告的工具
		}
		log.Printf("[Subagent] 🎭 使用具名 agent [%s]（工具 %v）\n", def.Name, def.Tools)
	}

	// 綁定技能：解析技能名取完整正文，注入子 agent 隔離 context。失敗則 error-as-observation，
	// 不中斷主循環，由主 agent 自行改用其他名稱或不綁定。
	var skillBody string
	if input.Skill != "" {
		body, err := t.skillLoader.ReadSkill(input.Skill)
		if err != nil {
			return fmt.Errorf("綁定技能失敗: %v", err).Error(), nil
		}
		skillBody = body
		log.Printf("[Subagent] 📎 綁定技能 [%s]（注入 %d 字元正文至子 context）\n", input.Skill, len(body))
	}

	log.Printf("[Subagent] 🚀 主 Agent 發起委派！正在拉起 [%s]: [%s]...\n", role, input.TaskPrompt)

	summary, err := t.runner.RunSub(ctx, input.TaskPrompt, skillBody, systemPrompt, reg, t.reporter)
	if err != nil {
		// 故意把錯誤 swallow 成正常 Output（error-as-observation）：讓主 agent 看到失敗信息
		// 但不中斷主 ReAct 循環，由主 agent 自行決定如何補救。
		return fmt.Errorf("子智能體執行失敗: %v", err).Error(), nil
	}

	log.Printf("[Subagent] ✅ 子智能體任務結束。報告返回給主幹...")

	return fmt.Sprintf("【子智能體探索報告】:\n%s", summary), nil
}
