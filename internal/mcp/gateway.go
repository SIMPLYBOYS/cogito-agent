package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// Gateway 是 MCP 工具的「漸進式暴露」閘道：不把 N 個 MCP 工具逐一放進 LLM 的 tools 清單
// （那會讓每輪重送大量 schema），而是只暴露兩個通用工具——
//   - mcp_call_tool(name, arguments)：description 內含輕量目錄（每工具一行），調用指定 MCP 工具。
//   - mcp_describe_tool(name)：按需返回某工具的完整 JSON Schema。
//
// 如此 context 只帶「2 個 gateway 定義 + N 行目錄」，full schema 用到才載入。
type Gateway struct {
	byName map[string]*mcpTool // exposedName（<server>__<tool>）→ 適配器
	order  []string            // 穩定排序的工具名，供目錄列出
}

// NewGateway 對每個已連線的 client 做 tools/list 並建立目錄。單個 client 列舉失敗則跳過該 client。
func NewGateway(ctx context.Context, clients []*Client) (*Gateway, error) {
	g := &Gateway{byName: make(map[string]*mcpTool)}
	for _, cl := range clients {
		ts, err := cl.Tools(ctx)
		if err != nil {
			continue // 跳過列舉失敗的 server，不拖垮整個 gateway
		}
		for _, t := range ts {
			g.byName[t.exposedName] = t
			g.order = append(g.order, t.exposedName)
		}
	}
	sort.Strings(g.order)
	return g, nil
}

// Count 回傳目錄中的工具總數。
func (g *Gateway) Count() int { return len(g.order) }

// Names 回傳目錄中所有工具的 exposed 名稱（已排序）。
func (g *Gateway) Names() []string {
	out := make([]string, len(g.order))
	copy(out, g.order)
	return out
}

// catalog 產生「- name: 短描述」的輕量目錄文字，放進 mcp_call_tool 的 description。
func (g *Gateway) catalog() string {
	var b strings.Builder
	for _, name := range g.order {
		b.WriteString(fmt.Sprintf("- %s: %s\n", name, oneLine(g.byName[name].description, 120)))
	}
	return b.String()
}

// Tools 回傳要註冊的 2 個 gateway 工具。
func (g *Gateway) Tools() []tools.BaseTool {
	return []tools.BaseTool{&callTool{g: g}, &describeTool{g: g}}
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	if i := strings.Index(s, ". "); i > 0 && i < max {
		return s[:i+1] // 取第一句
	}
	if len([]rune(s)) > max {
		return string([]rune(s)[:max]) + "…"
	}
	return s
}

// ---- mcp_call_tool ----

type callTool struct{ g *Gateway }

func (t *callTool) Name() string { return "mcp_call_tool" }

func (t *callTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: "mcp_call_tool",
		Description: "調用一個外部 MCP 工具。可用工具目錄（名稱: 說明）如下；需要精確參數時先用 mcp_describe_tool 查 schema：\n\n" +
			t.g.catalog(),
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name":      map[string]interface{}{"type": "string", "description": "要調用的 MCP 工具名（見本說明的目錄）"},
				"arguments": map[string]interface{}{"type": "object", "description": "傳給該工具的參數物件"},
			},
			"required": []string{"name"},
		},
	}
}

func (t *callTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}
	mt, ok := t.g.byName[in.Name]
	if !ok {
		return "", fmt.Errorf("未知的 MCP 工具 %q（請用 mcp_call_tool 說明中的目錄名稱）", in.Name)
	}
	if len(in.Arguments) == 0 {
		in.Arguments = json.RawMessage("{}")
	}
	return mt.Execute(ctx, in.Arguments)
}

// ---- mcp_describe_tool ----

type describeTool struct{ g *Gateway }

func (t *describeTool) Name() string { return "mcp_describe_tool" }

func (t *describeTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        "mcp_describe_tool",
		Description: "返回某個 MCP 工具的完整參數 JSON Schema 與說明（在 mcp_call_tool 調用前先查清楚參數結構）。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "MCP 工具名（見 mcp_call_tool 目錄）"},
			},
			"required": []string{"name"},
		},
	}
}

func (t *describeTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}
	mt, ok := t.g.byName[in.Name]
	if !ok {
		return "", fmt.Errorf("未知的 MCP 工具 %q", in.Name)
	}
	out := map[string]any{
		"name":         mt.exposedName,
		"description":  mt.description,
		"input_schema": mt.inputSchema,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
