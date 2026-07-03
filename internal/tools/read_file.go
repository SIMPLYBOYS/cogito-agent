package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

type ReadFileTool struct {
	workDir string
}

func NewReadFileTool(workDir string) *ReadFileTool {
	return &ReadFileTool{workDir: workDir}
}

func (t *ReadFileTool) Name() string {
	return "read_file"
}

func (t *ReadFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "讀取指定路徑的文件內容。請提供相對工作區的路徑。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要讀取的文件路徑，如 cmd/claw/main.go",
				},
			},
			"required": []string{"path"},
		},
	}
}

type readFileArgs struct {
	Path string `json:"path"`
}

func (t *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input readFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}

	fullPath, err := resolveInWorkDir(t.workDir, input.Path)
	if err != nil {
		return "", err
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return "", fmt.Errorf("打開文件失敗: %w", err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("讀取文件內容失敗: %w", err)
	}

	const maxLen = 8000
	if len(content) > maxLen {
		truncatedMsg := fmt.Sprintf("%s\n\n...[由於內容過長，已被系統截斷至前 %d 字節]...", string(content[:maxLen]), maxLen)
		return truncatedMsg, nil
	}

	return string(content), nil
}
