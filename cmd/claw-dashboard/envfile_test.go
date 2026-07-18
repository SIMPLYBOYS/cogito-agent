package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 安全底線：更新非祕密 key 時，祕密行與註解【逐字保留】，只有目標 key 被改，缺的 key 追加。
func TestUpdateEnvFile_PreservesSecrets(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	original := "# cogito env\n" +
		"ANTHROPIC_API_KEY=sk-ant-LIVE-SECRET-xyz\n" +
		"SLACK_BOT_TOKEN=xoxb-LIVE-SECRET\n" +
		"CLAUDE_MODEL=claude-opus-4-8\n" +
		"COGITO_MEMORY_SYNTH=\n"
	if err := os.WriteFile(p, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := updateEnvFile(p, map[string]string{
		"CLAUDE_MODEL":        "claude-haiku-4-5", // 改既有
		"COGITO_MEMORY_SYNTH": "1",                // 改既有（空→1）
		"COGITO_PROVIDER":     "claude",           // 追加新 key
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	s := string(got)

	// 祕密逐字保留
	if !strings.Contains(s, "ANTHROPIC_API_KEY=sk-ant-LIVE-SECRET-xyz") {
		t.Error("Anthropic 祕密行被動到了！")
	}
	if !strings.Contains(s, "SLACK_BOT_TOKEN=xoxb-LIVE-SECRET") {
		t.Error("Slack 祕密行被動到了！")
	}
	// 註解保留
	if !strings.Contains(s, "# cogito env") {
		t.Error("註解被弄丟")
	}
	// 目標 key 改了
	if !strings.Contains(s, "CLAUDE_MODEL=claude-haiku-4-5") {
		t.Error("CLAUDE_MODEL 沒更新")
	}
	if !strings.Contains(s, "COGITO_MEMORY_SYNTH=1") {
		t.Error("COGITO_MEMORY_SYNTH 沒更新")
	}
	// 新 key 追加
	if !strings.Contains(s, "COGITO_PROVIDER=claude") {
		t.Error("新 key 沒追加")
	}
	// 權限 0600
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf(".env 權限應 0600，got %o", fi.Mode().Perm())
	}
	// readEnvValue 帶回現值
	if v := readEnvValue(p, "CLAUDE_MODEL"); v != "claude-haiku-4-5" {
		t.Errorf("readEnvValue 應回 claude-haiku-4-5，got %q", v)
	}
}
