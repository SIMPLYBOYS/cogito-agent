package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 檔案工具不得寫入 agent 的控制面（.claw/）。少了這層，「產物只進暫存區、需人工放行」的整條
// HITL 鏈可以被一句 write_file 繞過——包含自己解除成本上限、自己排 cron。
func TestResolveForWrite_RejectsControlDir(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".claw", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	blocked := []string{
		".claw/config.json",       // 執行期護欄（能改＝能解除自己的上限）
		".claw/cron.json",         // 排程（能寫＝能自己排程）
		".claw/skills/x/SKILL.md", // 生效技能（本該經 governance 晉升）
		".claw/memory/note.md",    // 長期記憶
		".claw",                   // 目錄本身
		"./.claw/config.json",     // 等價寫法
		"sub/../.claw/cron.json",  // 繞路寫法
	}
	for _, p := range blocked {
		if _, err := resolveForWrite(ws, p); err == nil {
			t.Errorf("寫入 %q 應被拒", p)
		} else if !strings.Contains(err.Error(), "控制面") {
			t.Errorf("%q 的錯誤訊息應說明原因，得：%v", p, err)
		}
	}

	// 一般工作區檔案不可被誤擋
	for _, p := range []string{"main.go", "sub/dir/file.txt", ".clawless/ok.txt", "a.claw"} {
		if _, err := resolveForWrite(ws, p); err != nil {
			t.Errorf("一般路徑 %q 不該被擋：%v", p, err)
		}
	}
}

// 讀取控制面仍應允許——agent 需要看得到自己的技能/設定，危險的是「改」。
func TestResolveInWorkDir_StillAllowsReadingControlDir(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".claw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveInWorkDir(ws, ".claw/config.json"); err != nil {
		t.Errorf("讀取控制面不該被擋：%v", err)
	}
}
