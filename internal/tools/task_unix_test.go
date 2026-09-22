//go:build !windows

package tools

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
)

// 已結束（Wait 已返回）的任務，清理時不能再對它的組 ID 發訊號：組 ID 就是當初的 PID，組內沒人之後
// 系統可以把它發給不相干的行程，對舊 ID 送 SIGKILL 會殺掉別人的整組。PID 重用沒辦法在測試裡強制，
// 這裡用「不握管線的孫行程」當替身：任務已結束，但組 ID 上仍有行程——從 kernel 看，這和「ID 已被
// 別人拿去用」無從區分，清理時殺到它就等於會殺到重用者。
func TestTaskManager_KillAllSparesFinishedTaskGroup(t *testing.T) {
	tm := NewTaskManager(sandbox.HostExecutor{}, t.TempDir())
	id, err := tm.Start("sleep 30 >/dev/null 2>&1 &")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		out, _ := tm.Output(id)
		return strings.Contains(out, "已結束")
	}, "shell 退出、管線無人持有後任務應標為已結束")
	pgid := tm.get(id).cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	tm.KillAll()
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Fatalf("KillAll 對已結束任務的舊組 ID %d 送了訊號（組已不存在：%v）", pgid, err)
	}
	if out, _ := tm.Output(id); !strings.Contains(out, "已結束") {
		t.Errorf("正常結束的任務不該因 KillAll 改標成已被終止：%s", out)
	}
}

// /stop 收背景指令走註冊表：bash_background 是 BackgroundStopper，收掉還在跑的並回報幾個。
// 先前 /stop 只取消主任務，背景指令要等整個程式關閉（KillAll）才收。
func TestRegistry_StopBackgroundKillsTasks(t *testing.T) {
	tm := NewTaskManager(sandbox.HostExecutor{}, t.TempDir())
	reg := NewRegistry()
	for _, tt := range NewTaskTools(tm) {
		reg.Register(tt)
	}
	id, err := tm.Start("sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	got := reg.(interface{ StopBackground() []string }).StopBackground()
	if len(got) != 1 || got[0] != "1 個背景指令" {
		t.Fatalf("應回報收掉 1 個背景指令，got %q", got)
	}
	if out, _ := tm.Output(id); !strings.Contains(out, "已被終止") {
		t.Errorf("背景指令應被終止：%s", out)
	}
}
