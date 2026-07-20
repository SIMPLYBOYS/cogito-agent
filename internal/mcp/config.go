package mcp

import (
	"encoding/json"
	"fmt"
	"os"
)

// ServerConfig 描述一個 MCP 伺服器（與 Claude Desktop / .mcp.json 的 mcpServers 條目同構）。
// 兩種傳輸：
//   - stdio（預設）：填 command/args/env，啟動子行程。
//   - Streamable HTTP：填 url（或 type:"http"）+ 可選 headers（如 Authorization），連遠端端點。
type ServerConfig struct {
	Name    string            `json:"-"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Type    string            `json:"type"`    // "http" → Streamable HTTP；空/"stdio" → stdio
	URL     string            `json:"url"`     // Streamable HTTP 端點
	Headers map[string]string `json:"headers"` // HTTP 請求頭（如 {"Authorization":"Bearer sk-..."}）
}

// isHTTP 判斷此設定是否走 Streamable HTTP（有 url 或 type=http）。
func (c ServerConfig) isHTTP() bool {
	return c.URL != "" || c.Type == "http"
}

func (c ServerConfig) envSlice() []string {
	out := make([]string, 0, len(c.Env))
	for k, v := range c.Env {
		out = append(out, k+"="+v)
	}
	return out
}

// LoadConfig 讀取 .mcp.json 形式的設定：{"mcpServers": {"name": {"command","args","env"}}}。
// 回傳的每個 ServerConfig 已填好 Name。
func LoadConfig(path string) ([]ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		MCPServers map[string]ServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析 MCP 設定失敗: %w", err)
	}
	servers := make([]ServerConfig, 0, len(raw.MCPServers))
	for name, cfg := range raw.MCPServers {
		cfg.Name = name
		servers = append(servers, cfg)
	}
	return servers, nil
}
