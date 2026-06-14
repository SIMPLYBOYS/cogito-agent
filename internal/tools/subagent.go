package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/yourname/go-tiny-claw/internal/schema"
)

// AgentRunner 定义引擎向工具层暴露的"拉起子智能体"能力。接口定义在 tools 包（使用端），
// 这样 tools 不需要 import engine，避免循环依赖；*engine.AgentEngine 靠 duck typing 满足它。
type AgentRunner interface {
	RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry Registry, reporter interface{}) (string, error)
}

// SubagentTool 是一个标准 BaseTool：主 agent 调用它来派出一个受限的探索子 agent，
// 子 agent 在隔离的上下文里跑完，只回传一段精炼报告——主 agent 的 session 不被搜索过程污染。
type SubagentTool struct {
	runner           AgentRunner
	readOnlyRegistry Registry
	reporter         interface{} // 暂用 interface 规避包依赖；RunSub 内部断言回 engine.Reporter
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
		Description: "派出一个专门用于深度探索（Exploration）的子智能体。当你需要阅读大量代码、跨文件查找逻辑时请调用此工具。它在探索完毕后，会给你返回一份极度精炼的摘要报告。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_prompt": map[string]interface{}{
					"type":        "string",
					"description": "给子智能体下达的明确探索指令。",
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
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	log.Printf("[Subagent] 🚀 主 Agent 发起委派！正在拉起探路者: [%s]...\n", input.TaskPrompt)

	summary, err := t.runner.RunSub(ctx, input.TaskPrompt, t.readOnlyRegistry, t.reporter)
	if err != nil {
		// 故意把错误 swallow 成正常 Output（error-as-observation）：让主 agent 看到失败信息
		// 但不中断主 ReAct 循环，由主 agent 自行决定如何补救。
		return fmt.Errorf("子智能体执行失败: %v", err).Error(), nil
	}

	log.Printf("[Subagent] ✅ 子智能体任务结束。报告返回给主干...")

	return fmt.Sprintf("【子智能体探索报告】:\n%s", summary), nil
}
