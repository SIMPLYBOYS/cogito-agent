package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServer_Governance(t *testing.T) {
	ws := t.TempDir()
	claw := filepath.Join(ws, ".claw")
	skillDir := filepath.Join(claw, "skills-proposed", "run-go-tests")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(skillDir, "SKILL.md"),
		"---\nname: run-go-tests\ndescription: 跑 go test 並解析結果\n---\n正文…")
	mustWrite(t, filepath.Join(claw, "AGENTS.proposed.md"), "## 慣例\n- 用 pnpm 而非 npm")
	mustWrite(t, filepath.Join(claw, "config.proposed.json"), `{"current":{"max_turns":40}}`)

	t.Setenv("COGITO_ALLOWED_USERS", "telegram:771163423,slack:U0AB")
	t.Setenv("COGITO_ADMIN_USERS", "telegram:771163423")

	srv := newServer(nil, "", ws, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/governance", nil))
	if rec.Code != 200 {
		t.Fatalf("/governance → %d", rec.Code)
	}
	body := rec.Body.String()
	for _, w := range []string{
		"run-go-tests",             // 技能提案 name
		"跑 go test",                // 技能提案 description
		"用 pnpm",                   // 記憶提案預覽
		"max_turns",                // 調參提案內容
		"telegram:771163423",       // 授權名單 id
		"放行記憶",                     // 記憶提案的放行按鈕
		"/governance/apply-config", // 調參提案的放行表單 action
		"in-memory",                // 待審批誠實標示跨行程看不到
	} {
		if !strings.Contains(body, w) {
			t.Errorf("/governance 應含 %q", w)
		}
	}
	// 只顯示 id、不得洩漏任何 secret 值（此測沒放 secret，防呆檢查 CSP 仍在）
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Error("/governance 缺嚴格 CSP")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
