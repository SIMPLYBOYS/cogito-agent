package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// AgentRunner 定義引擎向工具層暴露的"拉起子智能體"能力。接口定義在 tools 包（使用端），
// 這樣 tools 不需要 import engine，避免循環依賴；*engine.AgentEngine 靠 duck typing 滿足它。
type AgentRunner interface {
	RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry Registry, reporter interface{}) (string, error)
}

// SubagentTool 是一個標準 BaseTool：主 agent 調用它來派出一個受限的探索子 agent，
// 子 agent 在隔離的上下文裡跑完，只回傳一段精煉報告——主 agent 的 session 不被搜索過程汙染。
type SubagentTool struct {
	runner           AgentRunner
	readOnlyRegistry Registry
	reporter         interface{} // 暫用 interface 規避包依賴；RunSub 內部斷言回 engine.Reporter
}

func NewSubagentTool(runner AgentRunner, readOnlyRegistry Registry, reporter interface{}) *SubagentTool {
	return &SubagentTool{
		runner:           runner,
		readOnlyRegistry: readOnlyRegistry,
		reporter:         reporter,
	}
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "派出一個專門用於深度探索（Exploration）的子智能體。當你需要閱讀大量代碼、跨文件查找邏輯時請調用此工具。它在探索完畢後，會給你返回一份極度精煉的摘要報告。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_prompt": map[string]interface{}{
					"type":        "string",
					"description": "給子智能體下達的明確探索指令。",
				},
			},
			"required": []string{"task_prompt"},
		},
	}
}

type subagentArgs struct {
	TaskPrompt string `json:"task_prompt"`
}

func (t *SubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input subagentArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析參數失敗: %w", err)
	}

	log.Printf("[Subagent] 🚀 主 Agent 發起委派！正在拉起探路者: [%s]...\n", input.TaskPrompt)

	summary, err := t.runner.RunSub(ctx, input.TaskPrompt, t.readOnlyRegistry, t.reporter)
	if err != nil {
		// 故意把錯誤 swallow 成正常 Output（error-as-observation）：讓主 agent 看到失敗信息
		// 但不中斷主 ReAct 循環，由主 agent 自行決定如何補救。
		return fmt.Errorf("子智能體執行失敗: %v", err).Error(), nil
	}

	log.Printf("[Subagent] ✅ 子智能體任務結束。報告返回給主幹...")

	return fmt.Sprintf("【子智能體探索報告】:\n%s", summary), nil
}
