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

// editMCPServer 改結構欄位（url/command/args/type），env/headers 及其祕密【逐字保留】。
func TestEditMCPServer_PreservesSecrets(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".mcp.json")
	seed := `{"mcpServers":{"gh":{"type":"http","url":"https://old/mcp/","headers":{"Authorization":"Bearer sk-SECRET"}}}}`
	if err := os.WriteFile(p, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	// 改 url，headers 保留
	if err := editMCPServer(p, "gh", "http", "https://new/mcp/", ""); err != nil {
		t.Fatal(err)
	}
	s := readFileStr(t, p)
	if !strings.Contains(s, "https://new/mcp/") || strings.Contains(s, "https://old/mcp/") {
		t.Error("url 沒正確更新")
	}
	if !strings.Contains(s, "Bearer sk-SECRET") {
		t.Error("編輯後 headers 祕密遺失了！")
	}

	// 切換 http→stdio：url 清掉、command 設好、祕密仍保留
	if err := editMCPServer(p, "gh", "stdio", "npx", "-y srv"); err != nil {
		t.Fatal(err)
	}
	s2 := readFileStr(t, p)
	if !strings.Contains(s2, "npx") || strings.Contains(s2, "https://new/mcp/") {
		t.Error("切到 stdio 後 command/url 不對")
	}
	if !strings.Contains(s2, "Bearer sk-SECRET") {
		t.Error("切換 type 後祕密仍應保留")
	}

	// 不存在 → 錯
	if err := editMCPServer(p, "nope", "stdio", "x", ""); err == nil {
		t.Error("不存在的 server 應回錯")
	}
}

func readFileStr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// MCP 祕密：讀單一 env/header 值；輪替只改該值、其餘欄位與其他祕密逐字保留。
func TestMCPSecretRotate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".mcp.json")
	seed := `{"mcpServers":{"gh":{"type":"http","url":"https://x/mcp/","headers":{"Authorization":"Bearer OLD","X-Other":"keep"}}}}`
	if err := os.WriteFile(p, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, ok := readMCPSecretValue(p, "gh", "headers", "Authorization"); !ok || v != "Bearer OLD" {
		t.Fatalf("reveal 應回現值，got %q %v", v, ok)
	}
	if err := setMCPSecretValue(p, "gh", "headers", "Authorization", "Bearer NEW"); err != nil {
		t.Fatal(err)
	}
	s := readFileStr(t, p)
	if !strings.Contains(s, "Bearer NEW") || strings.Contains(s, "Bearer OLD") {
		t.Error("Authorization 沒正確輪替")
	}
	if !strings.Contains(s, "X-Other") || !strings.Contains(s, "keep") {
		t.Error("其他 header 被動到了")
	}
	if !strings.Contains(s, "https://x/mcp/") {
		t.Error("url 被動到了")
	}
	if _, ok := readMCPSecretValue(p, "gh", "env", "NOPE"); ok {
		t.Error("不存在的 key 應回 false")
	}
}

// MCP 祕密 handler：同源回值；跨站 403；INSECURE（非 loopback）403。
func TestMCPSecret_HandlerGate(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")
	_ = os.WriteFile(mcpPath, []byte(`{"mcpServers":{"gh":{"type":"http","url":"https://x","headers":{"Authorization":"Bearer OLD"}}}}`), 0o600)
	t.Setenv("COGITO_MCP_CONFIG", mcpPath)
	t.Setenv("COGITO_DASH_INSECURE", "")
	srv := newServer(nil, "", dir, nil)
	rev := func(secFetch string) (int, string) {
		req := httptest.NewRequest("GET", "/mcp/secret/reveal?server=gh&kind=headers&key=Authorization", nil)
		if secFetch != "" {
			req.Header.Set("Sec-Fetch-Site", secFetch)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := rev("same-origin"); code != 200 || body != "Bearer OLD" {
		t.Errorf("同源應回值，got %d %q", code, body)
	}
	if code, _ := rev("cross-site"); code != 403 {
		t.Errorf("跨站應 403，got %d", code)
	}
	t.Setenv("COGITO_DASH_INSECURE", "1")
	if code, _ := rev("same-origin"); code != 403 {
		t.Errorf("INSECURE 應 403，got %d", code)
	}
}
