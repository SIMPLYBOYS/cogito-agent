package chatbot

import (
	"strings"
	"testing"
)

// try*Command 是自我進化的 apply/reject 閘，原本 0% 覆蓋。這裡驗證「無提案」分支與
// 「非指令不攔截」——用空 workDir（無 .claw 提案）觸發無提案路徑，不需 LLM/API。

func lastOf(msgs func() []string) string {
	all := msgs()
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

func TestTryConfigCommand(t *testing.T) {
	c, msgs := newCaptureCore(t, "cmdcfg", nil, nil)
	c.workDir = t.TempDir()

	if !c.tryConfigCommand("cmdcfg:ch", "apply config") {
		t.Fatal("apply config 應被消費（回 true）")
	}
	if got := lastOf(msgs); !strings.Contains(got, "沒有提案") {
		t.Errorf("無提案應回對應訊息，got %q", got)
	}
	if c.tryConfigCommand("cmdcfg:ch", "幫我重構這段") {
		t.Error("非指令不該被 config 閘攔截")
	}
}

func TestTryMemoryCommand(t *testing.T) {
	c, msgs := newCaptureCore(t, "cmdmem", nil, nil)
	c.workDir = t.TempDir()

	if !c.tryMemoryCommand("cmdmem:ch", "reject memory") {
		t.Fatal("reject memory 應被消費")
	}
	if got := lastOf(msgs); !strings.Contains(got, "沒有提案") {
		t.Errorf("無提案記憶應回對應訊息，got %q", got)
	}
	if c.tryMemoryCommand("cmdmem:ch", "just chatting") {
		t.Error("非指令不該被 memory 閘攔截")
	}
}

func TestTryEdgesCommand(t *testing.T) {
	c, msgs := newCaptureCore(t, "cmdedge", nil, nil)
	c.workDir = t.TempDir()

	if !c.tryEdgesCommand("cmdedge:ch", "apply edges") {
		t.Fatal("apply edges 應被消費")
	}
	if got := lastOf(msgs); !strings.Contains(got, "沒有提案") {
		t.Errorf("無提案關係應回對應訊息，got %q", got)
	}
	if c.tryEdgesCommand("cmdedge:ch", "看看這個 bug") {
		t.Error("非指令不該被 edges 閘攔截")
	}
}
