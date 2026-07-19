package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillsPage(t *testing.T) {
	ws := t.TempDir()
	skDir := filepath.Join(ws, ".claw", "skills", "deploy")
	if err := os.MkdirAll(skDir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: deploy-flow\ndescription: 一鍵部署到 staging\n---\n先跑測試，再 build，最後 push。\n"
	if err := os.WriteFile(filepath.Join(skDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newServer(nil, "", ws, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/skills", nil))

	body := rec.Body.String()
	for _, want := range []string{"deploy-flow", "一鍵部署到 staging", "先跑測試"} {
		if !strings.Contains(body, want) {
			t.Errorf("技能頁缺少 %q", want)
		}
	}
	// sidebar 應把 skills 標為 active
	if !strings.Contains(body, `href="/skills" class="on"`) {
		t.Error("sidebar 未把 skills 標為 active")
	}
}

func TestSkillsPage_Empty(t *testing.T) {
	srv := newServer(nil, "", t.TempDir(), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/skills", nil))
	if !strings.Contains(rec.Body.String(), "尚無技能") {
		t.Error("空技能目錄應顯示提示")
	}
}
