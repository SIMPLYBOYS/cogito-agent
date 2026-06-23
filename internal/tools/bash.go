package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

type BashTool struct {
	workDir  string
	executor sandbox.Executor
}

// NewBashTool 預設用 HostExecutor（宿主機執行，原行為不變）。
func NewBashTool(workDir string) *BashTool {
	return &BashTool{workDir: workDir, executor: sandbox.HostExecutor{}}
}

// NewBashToolWithExecutor 注入自訂 Executor（如 DockerExecutor），把命令丟進隔離環境執行。
func NewBashToolWithExecutor(workDir string, ex sandbox.Executor) *BashTool {
	if ex == nil {
		ex = sandbox.HostExecutor{}
	}
	return &BashTool{workDir: workDir, executor: ex}
}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "在當前工作區執行任意的 bash 命令。支持鏈式命令(如 &&)。返回標準輸出(stdout)和標準錯誤(stderr)。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "要執行的 bash 命令",
				},
			},
			"required": []string{"command"},
		},
	}
}

type bashArgs struct {
	Command string `json:"command"`
}

func (t *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input bashArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := t.executor.Run(timeoutCtx, input.Command, t.workDir)
	outputStr := string(out)

	if timeoutCtx.Err() == context.DeadlineExceeded {
		return outputStr + "\n[警告: 命令執行超時(30s)，已被系統強制終止。]", nil
	}

	if err != nil {
		return fmt.Sprintf("執行報錯: %v\n輸出:\n%s", err, outputStr), nil
	}

	if outputStr == "" {
		return "命令執行成功，無終端輸出。", nil
	}

	const maxLen = 8000
	if len(outputStr) > maxLen {
		return fmt.Sprintf("%s\n\n...[終端輸出過長，已截斷至前 %d 字節]...", outputStr[:maxLen], maxLen), nil
	}

	return outputStr, nil
}
