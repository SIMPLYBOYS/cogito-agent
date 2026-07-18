package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/mcp"
)

const (
	mcpEnabledKey  = "mcpServers"
	mcpDisabledKey = "mcpServersDisabled" // mcp.LoadConfig 只讀 mcpServers，故移到這＝停用（不必改 LoadConfig）
)

// mcpNameRe 限制 server 名為安全識別字（英數 + -_，≤64）——它會當成工具前綴與 JSON key。
var mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// mcpServerRow 是 UI 顯示用（值遮罩：env/headers 可能含祕密，只標 HasSecrets、不外洩值）。
type mcpServerRow struct {
	Name, Type, Target string
	HasSecrets         bool
	Disabled           bool
}

// mcpConfigPath 回目前 .mcp.json 路徑（COGITO_MCP_CONFIG，預設 ./.mcp.json）。
func mcpConfigPath() string {
	if p := strings.TrimSpace(os.Getenv("COGITO_MCP_CONFIG")); p != "" {
		return p
	}
	return ".mcp.json"
}

// readMCPFile 讀整個 .mcp.json 成 top-level map（保留未知 key）。缺檔回空 map。
func readMCPFile(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("解析 .mcp.json 失敗: %w", err)
	}
	if top == nil {
		top = map[string]json.RawMessage{}
	}
	return top, nil
}

// writeMCPFile 原子寫回（0600——env/headers 可能含祕密）。json.Marshal 對 map key 排序，輸出穩定。
func writeMCPFile(path string, top map[string]json.RawMessage) error {
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// serverMap 取某 key 底下的 <name>→raw 條目（raw 逐字保留，故 env/headers 等欄位不會在增刪/切換時遺失）。
func serverMap(top map[string]json.RawMessage, key string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if raw, ok := top[key]; ok {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func putServerMap(top map[string]json.RawMessage, key string, m map[string]json.RawMessage) {
	if len(m) == 0 {
		delete(top, key)
		return
	}
	b, _ := json.Marshal(m)
	top[key] = b
}

// readMCPServers 列出啟用 + 停用的 server（供顯示；env/headers 值不外洩，只標 HasSecrets）。
func readMCPServers(path string) ([]mcpServerRow, error) {
	top, err := readMCPFile(path)
	if err != nil {
		return nil, err
	}
	var rows []mcpServerRow
	collect := func(key string, disabled bool) {
		for name, raw := range serverMap(top, key) {
			var c mcp.ServerConfig
			_ = json.Unmarshal(raw, &c)
			rows = append(rows, mcpServerRow{
				Name: name, Type: serverType(c), Target: serverTarget(c),
				HasSecrets: len(c.Env) > 0 || len(c.Headers) > 0, Disabled: disabled,
			})
		}
	}
	collect(mcpEnabledKey, false)
	collect(mcpDisabledKey, true)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func serverType(c mcp.ServerConfig) string {
	if c.URL != "" || c.Type == "http" {
		return "http"
	}
	return "stdio"
}

func serverTarget(c mcp.ServerConfig) string {
	if c.URL != "" {
		return c.URL
	}
	t := c.Command
	if len(c.Args) > 0 {
		t += " " + strings.Join(c.Args, " ")
	}
	return t
}

// addMCPServer 新增一個 server（僅 command/url + args——刻意不收 env/headers，避免經手祕密；需要祕密
// 的 server 請手動編 .mcp.json）。名稱不可與現有（含停用）重複。
func addMCPServer(path, name, typ, target, argsStr string) error {
	name = strings.TrimSpace(name)
	if !mcpNameRe.MatchString(name) {
		return fmt.Errorf("無效的 server 名（只允許英數與 -_，≤64 字）")
	}
	target = strings.TrimSpace(target)
	top, err := readMCPFile(path)
	if err != nil {
		return err
	}
	if _, ok := serverMap(top, mcpEnabledKey)[name]; ok {
		return fmt.Errorf("server %q 已存在", name)
	}
	if _, ok := serverMap(top, mcpDisabledKey)[name]; ok {
		return fmt.Errorf("server %q 已存在（停用中）", name)
	}

	var cfg map[string]any
	if typ == "http" {
		if target == "" {
			return fmt.Errorf("http server 需要 url")
		}
		cfg = map[string]any{"type": "http", "url": target}
	} else {
		if target == "" {
			return fmt.Errorf("stdio server 需要 command")
		}
		cfg = map[string]any{"command": target}
		if fields := strings.Fields(argsStr); len(fields) > 0 {
			cfg["args"] = fields
		}
	}
	raw, _ := json.Marshal(cfg)
	m := serverMap(top, mcpEnabledKey)
	m[name] = raw
	putServerMap(top, mcpEnabledKey, m)
	return writeMCPFile(path, top)
}

// removeMCPServer 從啟用與停用兩處刪掉指定 server。
func removeMCPServer(path, name string) error {
	top, err := readMCPFile(path)
	if err != nil {
		return err
	}
	for _, key := range []string{mcpEnabledKey, mcpDisabledKey} {
		m := serverMap(top, key)
		if _, ok := m[name]; ok {
			delete(m, name)
			putServerMap(top, key, m)
		}
	}
	return writeMCPFile(path, top)
}

// toggleMCPServer 在 mcpServers ↔ mcpServersDisabled 之間搬移（停用不刪設定，raw 逐字保留）。
func toggleMCPServer(path, name string) error {
	top, err := readMCPFile(path)
	if err != nil {
		return err
	}
	en := serverMap(top, mcpEnabledKey)
	dis := serverMap(top, mcpDisabledKey)
	switch {
	case en[name] != nil:
		dis[name] = en[name]
		delete(en, name)
	case dis[name] != nil:
		en[name] = dis[name]
		delete(dis, name)
	default:
		return fmt.Errorf("找不到 server %q", name)
	}
	putServerMap(top, mcpEnabledKey, en)
	putServerMap(top, mcpDisabledKey, dis)
	return writeMCPFile(path, top)
}
