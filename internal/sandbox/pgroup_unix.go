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

// killGroup 殺掉整個 process group（負號 pid ＝ 整組）。取不到 pgid 就退回只殺直接子行程——
// 聊勝於無，而且那通常表示行程已經結束了。
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
