package chatbot

import (
	"context"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
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

func TestTryHelpCommand(t *testing.T) {
	c, msgs := newCaptureCore(t, "cmdhelp", nil, nil)
	for _, cmd := range []string{"help", "/help", "?", "指令", "commands"} {
		if !c.tryHelpCommand("cmdhelp:ch", cmd) {
			t.Errorf("%q 應被 help 閘攔截", cmd)
		}
	}
	last := lastOf(msgs)
	if !strings.Contains(last, "指令一覽") || !strings.Contains(last, "approve") || !strings.Contains(last, "plan on") {
		t.Errorf("help 應列出指令，got %q", last)
	}
	if c.tryHelpCommand("cmdhelp:ch", "幫我修個 bug") {
		t.Error("一般訊息不該被 help 閘攔截")
	}
}

func TestTryStopCommand(t *testing.T) {
	c, msgs := newCaptureCore(t, "cmdstop", nil, nil)
	// 沒有執行中 → 提示無任務
	if !c.tryStopCommand("cmdstop:ch", "stop") {
		t.Fatal("stop 應被攔截")
	}
	if !strings.Contains(lastOf(msgs), "沒有正在執行") {
		t.Errorf("無任務時應提示，got %q", lastOf(msgs))
	}
	// 有執行中 → 中止
	wd := c.channelWorkDir("cmdstop:ch")
	c.tryAcquire(wd, func() {})
	t.Cleanup(func() { c.release(wd) }) // running 是 package 級的：不還原則 -count=2 撞殘留
	c.tryStopCommand("cmdstop:ch", "/stop")
	if !strings.Contains(lastOf(msgs), "中止") {
		t.Errorf("有任務時應提示中止，got %q", lastOf(msgs))
	}
	if c.tryStopCommand("cmdstop:ch", "幫我停車") {
		t.Error("非指令不該被 stop 閘攔截")
	}
}

func TestTryStatusCommand(t *testing.T) {
	c, msgs := newCaptureCore(t, "cmdstat", nil, nil)
	if !c.tryStatusCommand("cmdstat:ch", "status") {
		t.Fatal("status 應被攔截")
	}
	got := lastOf(msgs)
	if !strings.Contains(got, "本會話狀態") || !strings.Contains(got, "累計花費") {
		t.Errorf("status 內容錯: %q", got)
	}
}

func TestTryModelCommand(t *testing.T) {
	c, msgs := newCaptureCore(t, "cmdmodel", nil, nil)
	// 查看 → 顯示預設
	if !c.tryModelCommand("cmdmodel:ch", "model") {
		t.Fatal("model 應被攔截")
	}
	if !strings.Contains(lastOf(msgs), "預設") {
		t.Errorf("查看應顯示預設，got %q", lastOf(msgs))
	}
	// 設定
	c.tryModelCommand("cmdmodel:ch", "model claude-haiku-4-5")
	if c.sessionFor("cmdmodel:ch").Model() != "claude-haiku-4-5" {
		t.Error("model <id> 應設定 session 模型")
	}
	// 還原
	c.tryModelCommand("cmdmodel:ch", "model reset")
	if c.sessionFor("cmdmodel:ch").Model() != "" {
		t.Error("model reset 應清空")
	}
	// 非指令（不以 model 開頭）
	if c.tryModelCommand("cmdmodel:ch", "幫我看一下 model 的架構") {
		t.Error("非指令不該被 model 閘攔截")
	}
}

func TestTryCompressCommand_BusyRejected(t *testing.T) {
	c, msgs := newCaptureCore(t, "cmdcomp", nil, nil)
	wd := c.channelWorkDir("cmdcomp:ch")
	c.tryAcquire(wd, func() {}) // 佔住鎖
	t.Cleanup(func() { c.release(wd) })
	if !c.tryCompressCommand("cmdcomp:ch", "compress") {
		t.Fatal("compress 應被攔截")
	}
	if !strings.Contains(lastOf(msgs), "任務進行中") {
		t.Errorf("忙碌時應拒絕壓縮，got %q", lastOf(msgs))
	}
	if c.tryCompressCommand("cmdcomp:ch", "壓一下這段話") {
		t.Error("非指令不該被 compress 閘攔截")
	}
}

func TestTryLearnCommand(t *testing.T) {
	// 未接 hook → 提示未啟用，但仍消費指令
	c, msgs := newCaptureCore(t, "cmdlearn", nil, nil)
	if !c.tryLearnCommand("cmdlearn:ch", "learn") {
		t.Fatal("learn 應被攔截")
	}
	if !strings.Contains(lastOf(msgs), "未接") {
		t.Errorf("未接 hook 應提示，got %q", lastOf(msgs))
	}
	if c.tryLearnCommand("cmdlearn:ch", "學一下這門技術") {
		t.Error("非指令不該被 learn 閘攔截")
	}

	// 接了 hook 但忙碌 → 拒絕
	c2, msgs2 := newCaptureCore(t, "cmdlearn2", nil, nil)
	c2.learn = func(context.Context, *ctxpkg.Session) (string, error) { return "x", nil }
	c2.tryAcquire(c2.channelWorkDir("cmdlearn2:ch"), func() {})
	t.Cleanup(func() { c2.release(c2.channelWorkDir("cmdlearn2:ch")) })
	c2.tryLearnCommand("cmdlearn2:ch", "learn")
	if !strings.Contains(lastOf(msgs2), "任務進行中") {
		t.Errorf("忙碌時 learn 應拒絕，got %q", lastOf(msgs2))
	}
}

func TestTryGoalCommand(t *testing.T) {
	c, msgs := newCaptureCore(t, "cmdgoal", nil, nil)
	c.workDir = t.TempDir()
	conv := "cmdgoal:ch"

	// 無 goal → status 提示沒設
	c.tryGoalCommand(conv, "goal")
	if !strings.Contains(lastOf(msgs), "沒有設定目標") {
		t.Errorf("空 goal 應提示未設定，got %q", lastOf(msgs))
	}

	// 設 goal：先佔鎖走 busy 分支（不實跑，免 factory），驗證目標已存進 session
	c.tryAcquire(c.channelWorkDir(conv), func() {})
	t.Cleanup(func() { c.release(c.channelWorkDir(conv)) })
	if !c.tryGoalCommand(conv, "goal 通過所有測試") {
		t.Fatal("goal <text> 應被攔截")
	}
	if got := c.sessionFor(conv).Goal(); got != "通過所有測試" {
		t.Errorf("目標應被設定，got %q", got)
	}
	if !strings.Contains(lastOf(msgs), "任務進行中") {
		t.Errorf("忙碌時 goal <text> 應提示，got %q", lastOf(msgs))
	}
	c.release(c.channelWorkDir(conv))

	// status 顯示目標；pause/resume/clear
	c.tryGoalCommand(conv, "goal status")
	if !strings.Contains(lastOf(msgs), "通過所有測試") {
		t.Errorf("status 應顯示目標，got %q", lastOf(msgs))
	}
	c.tryGoalCommand(conv, "goal pause")
	if !c.sessionFor(conv).GoalPaused() {
		t.Error("pause 應設暫停")
	}
	c.tryGoalCommand(conv, "goal resume")
	if c.sessionFor(conv).GoalPaused() {
		t.Error("resume 應解除暫停")
	}
	c.tryGoalCommand(conv, "goal clear")
	if c.sessionFor(conv).Goal() != "" {
		t.Error("clear 應清除目標")
	}

	if c.tryGoalCommand(conv, "幫我看看程式") {
		t.Error("非指令不該被 goal 閘攔截")
	}
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
