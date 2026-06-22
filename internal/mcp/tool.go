package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// mcpTool 把一個遠端 MCP 工具適配成本專案的 tools.BaseTool。對外暴露的名字加上 server 前綴
// （避免與內建工具/其他 server 撞名），但呼叫遠端時用原始名 remoteName。
type mcpTool struct {
	client      *Client
	remoteName  string
	exposedName string
	description string
	inputSchema map[string]interface{}
}

func (t *mcpTool) Name() string { return t.exposedName }

func (t *mcpTool) Definition() schema.ToolDefinition {
	schemaObj := t.inputSchema
	if schemaObj == nil {
		schemaObj = map[string]interface{}{"type": "object"}
	}
	return schema.ToolDefinition{
		Name:        t.exposedName,
		Description: t.description,
		InputSchema: schemaObj,
	}
}

func (t *mcpTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var argMap map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return "", fmt.Errorf("MCP 工具參數解析失敗: %w", err)
		}
	}
	return t.client.callTool(ctx, t.remoteName, argMap)
}

// Tools 完成 tools/list 並把該 server 的工具適配成 BaseTool 列表（名字前綴為「<server>__」）。
func (c *Client) Tools(ctx context.Context) ([]*mcpTool, error) {
	specs, err := c.listTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*mcpTool, 0, len(specs))
	for _, s := range specs {
		out = append(out, &mcpTool{
			client:      c,
			remoteName:  s.Name,
			exposedName: c.Name + "__" + s.Name,
			description: s.Description,
			inputSchema: s.InputSchema,
		})
	}
	return out, nil
}
