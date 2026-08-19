package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 剛 clone 的機器（沒有 .claw/agents/）必須派得動出貨技能引用的那七個。
//
// 這是本次改動的整個理由：orchestrate 是版控過的技能，裡面寫著「就一定要派 spec」；
// 而人設是每人各自的設定、刻意不進版。若功能型 agent 也不出貨，版控的技能就會引用
// 目標機器上不存在的名字——委派直接失敗（subagent.go 對載入失敗回真錯誤），
// 也就是 clone 下來即是壞的。
func TestAgentLoader_BuiltinsAvailableWithoutDisk(t *testing.T) {
	l := NewAgentLoader(t.TempDir()) // 完全沒有 .claw/agents/
	for _, name := range []string{"spec", "planner", "implementer", "code-reviewer",
		"correctness", "performance", "security-auditor"} {
		d, err := l.Load(name)
		if err != nil {
			t.Errorf("內建 agent %q 載不到：%v", name, err)
			continue
		}
		if strings.TrimSpace(d.Prompt) == "" {
			t.Errorf("內建 agent %q 沒有 system prompt", name)
		}
	}
	// 索引也要看得到，否則模型不知道能派誰。
	if idx := l.Index(); !strings.Contains(idx, "spec") {
		t.Errorf("Index 沒有列出內建 agent：%q", idx)
	}
}

// 磁碟優先：使用者放同名檔就覆蓋內建，不需要任何開關。
func TestAgentLoader_DiskOverridesBuiltin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claw", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const mark = "這是使用者自己的版本"
	if err := os.WriteFile(filepath.Join(dir, "spec.md"),
		[]byte("---\nname: spec\ndescription: 客製\n---\n"+mark), 0o644); err != nil {
		t.Fatal(err)
	}

	l := NewAgentLoader(root)
	d, err := l.Load("spec")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Prompt, mark) {
		t.Error("磁碟上的版本沒有覆蓋掉內建——使用者改不動出貨的預設")
	}
	// 覆蓋之後索引裡不能同時出現兩個 spec。
	if n := strings.Count(l.Index(), "spec"); n != 1 {
		t.Errorf("Index 出現 %d 次 spec，應只有 1（磁碟與內建重複列出）", n)
	}
}

// 不存在又不是內建的，仍要回錯——退回機制不能把打錯字變成靜默成功。
func TestAgentLoader_UnknownStillErrors(t *testing.T) {
	if _, err := NewAgentLoader(t.TempDir()).Load("nosuchagent"); err == nil {
		t.Error("未知 agent 應回錯")
	}
}
