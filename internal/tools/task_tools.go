package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// NewTaskTools 回傳一組操作背景任務的工具（共用同一個 session 級 TaskManager）：
// bash_background（拉起）/ task_output（查輸出）/ task_kill（終止）/ task_list（列出）。
func NewTaskTools(tm *TaskManager) []BaseTool {
	return []BaseTool{
		&bashBackgroundTool{tm: tm},
		&taskOutputTool{tm: tm},
		&taskKillTool{tm: tm},
		&taskListTool{tm: tm},
	}
}

// ---- bash_background ----

type bashBackgroundTool struct{ tm *TaskManager }

func (t *bashBackgroundTool) Name() string { return "bash_background" }

func (t *bashBackgroundTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "在背景啟動一條長時間執行的 bash 命令（如 dev server、長時間建置/訓練），立即回傳任務 ID，不受一般 bash 的 30 秒逾時限制。之後用 task_output 查輸出、task_kill 終止。注意：背景任務不會自動結束，用完請 task_kill。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string", "description": "要在背景執行的 bash 命令"},
			},
			"required": []string{"command"},
		},
	}
}

func (t *bashBackgroundTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}
	id, err := t.tm.Start(in.Command)
	if err != nil {
		return fmt.Errorf("無法啟動背景任務: %v", err).Error(), nil // error-as-observation
	}
	return fmt.Sprintf("已在背景啟動任務 %s：%s\n用 task_output(\"%s\") 查看輸出、task_kill(\"%s\") 終止。", id, in.Command, id, id), nil
}

// ---- task_output ----

type taskOutputTool struct{ tm *TaskManager }

func (t *taskOutputTool) Name() string { return "task_output" }

func (t *taskOutputTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "查看某背景任務目前累積的輸出與狀態（執行中 / 已結束 / 已終止）。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_id": map[string]interface{}{"type": "string", "description": "bash_background 回傳的任務 ID"},
			},
			"required": []string{"task_id"},
		},
	}
}

func (t *taskOutputTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}
	out, err := t.tm.Output(in.TaskID)
	if err != nil {
		return err.Error(), nil // error-as-observation
	}
	return out, nil
}

// ---- task_kill ----

type taskKillTool struct{ tm *TaskManager }

func (t *taskKillTool) Name() string { return "task_kill" }

func (t *taskKillTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "終止一個背景任務。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_id": map[string]interface{}{"type": "string", "description": "要終止的任務 ID"},
			},
			"required": []string{"task_id"},
		},
	}
}

func (t *taskKillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}
	if err := t.tm.Kill(in.TaskID); err != nil {
		return err.Error(), nil // error-as-observation
	}
	return fmt.Sprintf("已終止背景任務 %s。", in.TaskID), nil
}

// ---- task_list ----

type taskListTool struct{ tm *TaskManager }

func (t *taskListTool) Name() string { return "task_list" }

func (t *taskListTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "列出目前所有背景任務及其狀態。",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

func (t *taskListTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return t.tm.List(), nil
}
