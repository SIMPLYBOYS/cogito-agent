package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/mcp"
)

func TestMCPFile_AddRemoveToggle(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".mcp.json")
	// seed 一個【含祕密 headers】的 http server，驗證增刪/切換不遺失
	seed := `{"mcpServers":{"twinkle":{"type":"http","url":"https://api.x/mcp/","headers":{"Authorization":"Bearer sk-SECRET"}}}}`
	if err := os.WriteFile(p, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	// 列表：type=http、標 HasSecrets、值不外洩
	rows, _ := readMCPServers(p)
	if len(rows) != 1 || rows[0].Type != "http" || !rows[0].HasSecrets {
		t.Fatalf("list 不對：%+v", rows)
	}

	// 新增 stdio
	if err := addMCPServer(p, "fs", "stdio", "npx", "-y @mcp/server-fs"); err != nil {
		t.Fatal(err)
	}
	// 非法名 / 重複名 → 拒
	if err := addMCPServer(p, "../evil", "stdio", "x", ""); err == nil {
		t.Error("非法 server 名應被拒")
	}
	if err := addMCPServer(p, "twinkle", "http", "https://y", ""); err == nil {
		t.Error("重複名應被拒")
	}

	// 停用 twinkle → 祕密必須存活
	if err := toggleMCPServer(p, "twinkle"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "Bearer sk-SECRET") {
		t.Error("停用後 headers 祕密遺失了！")
	}
	// LoadConfig 只看啟用的：停用的 twinkle 不該被載入，fs 該在
	loaded, _ := mcp.LoadConfig(p)
	var names []string
	for _, c := range loaded {
		names = append(names, c.Name)
	}
	if strings.Contains(strings.Join(names, ","), "twinkle") {
		t.Errorf("停用的 server 不該被 LoadConfig 載入，got %v", names)
	}
	if !strings.Contains(strings.Join(names, ","), "fs") {
		t.Errorf("啟用的 fs 應被載入，got %v", names)
	}

	// 移除 fs
	if err := removeMCPServer(p, "fs"); err != nil {
		t.Fatal(err)
	}
	rows3, _ := readMCPServers(p)
	for _, r := range rows3 {
		if r.Name == "fs" {
			t.Error("fs 應已移除")
		}
	}
}

// MCP 編輯 handler 的 CSRF：跨站 POST 被擋。
func TestMCPEdit_CSRF(t *testing.T) {
	t.Setenv("COGITO_MCP_CONFIG", filepath.Join(t.TempDir(), ".mcp.json"))
	srv := newServer(nil, "", t.TempDir(), nil)
	for _, path := range []string{"/mcp/add", "/mcp/remove", "/mcp/toggle"} {
		req := httptest.NewRequest("POST", path, strings.NewReader("name=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Errorf("跨站 %s 應 403，got %d", path, rec.Code)
		}
	}
}
