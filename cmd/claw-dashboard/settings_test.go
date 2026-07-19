package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// POST /env-config：跨站被擋；同源則寫 .env（祕密逐字保留、toggle 寫 1），CSRF 生效。
func TestEnvConfigSave_WritesEnvPreservesSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // 讓 handler 的 ".env" 解到 temp
	t.Cleanup(func() {
		for k := range allowedEnvKeys {
			os.Unsetenv(k) // 清掉 handler os.Setenv 的殘留，避免污染其他測試
		}
	})
	if err := os.WriteFile(".env", []byte("ANTHROPIC_API_KEY=sk-LIVE-SECRET\nCLAUDE_MODEL=claude-opus-4-8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newServer(nil, "", dir, nil)

	// 跨站 → 403（不寫）
	req := httptest.NewRequest("POST", "/env-config", strings.NewReader("CLAUDE_MODEL=hacked"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("跨站 /env-config 應 403，got %d", rec.Code)
	}

	// 同源 → 303 + 寫入（_fields 宣告本表單負責的 key）
	req2 := httptest.NewRequest("POST", "/env-config", strings.NewReader("_fields=CLAUDE_MODEL+COGITO_MEMORY_SYNTH+COGITO_PROVIDER&CLAUDE_MODEL=claude-haiku-4-5&COGITO_MEMORY_SYNTH=1&COGITO_PROVIDER=claude"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("Sec-Fetch-Site", "same-origin")
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != 303 {
		t.Fatalf("同源 /env-config 應 303，got %d", rec2.Code)
	}
	got, _ := os.ReadFile(".env")
	s := string(got)
	if !strings.Contains(s, "ANTHROPIC_API_KEY=sk-LIVE-SECRET") {
		t.Error("祕密行被動到了！")
	}
	if !strings.Contains(s, "CLAUDE_MODEL=claude-haiku-4-5") {
		t.Error("CLAUDE_MODEL 沒更新")
	}
	if !strings.Contains(s, "COGITO_MEMORY_SYNTH=1") {
		t.Error("toggle 沒寫入")
	}
}

// _fields 局部更新：只帶 CLAUDE_MODEL 的表單，不該清掉其他 key（也不碰 _fields 外的欄位）。
func TestEnvConfigSave_PartialUpdate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Cleanup(func() {
		os.Unsetenv("CLAUDE_MODEL")
		os.Unsetenv("COGITO_ALLOWED_USERS")
	})
	if err := os.WriteFile(".env", []byte("COGITO_ALLOWED_USERS=alice\nCLAUDE_MODEL=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newServer(nil, "", dir, nil)

	// _fields 只宣告 CLAUDE_MODEL；即便 body 夾了 COGITO_ALLOWED_USERS= 也不該動它
	body := "_fields=CLAUDE_MODEL&CLAUDE_MODEL=new&COGITO_ALLOWED_USERS="
	req := httptest.NewRequest("POST", "/env-config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 303 {
		t.Fatalf("應 303，got %d", rec.Code)
	}
	got, _ := os.ReadFile(".env")
	s := string(got)
	if !strings.Contains(s, "CLAUDE_MODEL=new") {
		t.Error("CLAUDE_MODEL 應更新")
	}
	if !strings.Contains(s, "COGITO_ALLOWED_USERS=alice") {
		t.Error("不在 _fields 的 key 被清掉了——局部更新失效！")
	}
}

// 存檔回饋要分情況：cron 設定是本行程使用時才讀 → 立刻生效；沾到 bot 消費的設定才要重啟。
// 講錯會害使用者白重啟一次。
func TestEnvSaveMessage(t *testing.T) {
	live := envSaveMessage(map[string]string{cronTZKey: "Asia/Taipei", notifyTargetKey: "telegram:1"})
	if !strings.Contains(live, "立即生效") || strings.Contains(live, "需【重啟】") {
		t.Errorf("純 cron 設定應說立即生效、不得要求重啟，得：%s", live)
	}

	needsRestart := envSaveMessage(map[string]string{"COGITO_SANDBOX": "docker"})
	if !strings.Contains(needsRestart, "重啟") {
		t.Errorf("bot 消費的設定應提醒重啟，得：%s", needsRestart)
	}

	// 混合：只要沾到一個需重啟的，就得提醒
	mixed := envSaveMessage(map[string]string{cronTZKey: "UTC", "COGITO_SANDBOX": "docker"})
	if !strings.Contains(mixed, "重啟") {
		t.Errorf("混合時應提醒重啟，得：%s", mixed)
	}
}
