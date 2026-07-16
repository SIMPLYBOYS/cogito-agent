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

	// 只讀「上限 + 一點」而不是整檔：原本 io.ReadAll 把整個檔案吃進記憶體才截斷到 8000，
	// 那個上限只限制【回給 LLM 的量】、不限制【讀進記憶體的量】——agent 自己用 bash 造一個
	// 2GB 的 log 再 read_file 它，就是 2GB 常駐（且 read_file 不算危險操作，走不到審批）。
	// 多讀的餘量是為了判斷「有沒有被截斷」；UTF-8 一字最多 4 bytes，故以 4 倍上限為界。
	const maxRunes = 8000
	const readLimit = maxRunes*4 + 1
	content, err := io.ReadAll(io.LimitReader(file, readLimit))
	if err != nil {
		return "", fmt.Errorf("讀取文件內容失敗: %w", err)
	}

	// 按【字元】截斷：byte 切會切在多位元組字元中間，讀中文檔就吐非法 UTF-8 給模型。
	s := string(content)
	if truncated := schema.TruncRunes(s, maxRunes, ""); truncated != s {
		return fmt.Sprintf("%s\n\n...[由於內容過長，已被系統截斷至前 %d 字元]...", truncated, maxRunes), nil
	}
	return s, nil
}
