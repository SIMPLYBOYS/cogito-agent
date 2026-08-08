//go:build windows

package sandbox

import "os/exec"

// Windows 沒有 process group 的同語意（要 Job Object 才做得到整棵樹）。這裡維持原行為：
// 只殺直接子行程。本專案的部署目標是 unix，這個檔案存在只為了讓 windows 編得過。
func setPgid(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
