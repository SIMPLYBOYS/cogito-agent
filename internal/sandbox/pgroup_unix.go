//go:build !windows

package sandbox

import (
	"os/exec"
	"syscall"
)

// setPgid 讓命令自成一個 process group——逾時時才殺得掉【整棵】子孫樹。
// 只殺直接子行程沒有用：`bash -c "find / ..."` 裡的 find 是孫子，殺掉 bash 之後它還活著，
// 而且還握著 stdout 的寫入端。
func setPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup 殺掉整個 process group（負號 pid ＝ 整組）。
//
// pgid 不能在這時才用 Getpgid(shell PID) 查：`cmd &` 這種形狀 shell 會先退出、被 Wait 收屍，
// 之後就查不到了，只剩孫行程握著管線活著。setPgid 用 Pgid 0，組 ID 就是當初的 PID，
// 而組內還有人活著，這個 ID 就不會被系統重用——直接拿 PID 殺整組即可。
// 沒 setPgid 過的命令不能這樣做（它和我們同組，負號 PID 殺不到或殺錯），只殺直接子行程。
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
