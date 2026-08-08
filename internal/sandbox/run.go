package sandbox

import (
	"bytes"
	"context"
	"os/exec"
)

// runCombined 執行一條命令、回傳合併的 stdout+stderr，並在 ctx 逾時／取消時【殺掉整個 process
// group】。兩種 Executor 共用它，逾時語意才不會依實作而異。
//
// 為什麼不直接用 cmd.CombinedOutput()：它等的是「輸出管線關閉」，不是「子行程結束」。
// exec.CommandContext 的逾時只殺得掉直接子行程（bash），孫子（find / npm / 卡住的 curl）
// 還活著並繼續握著管線的寫入端，於是 Wait 一路等到孫子自己跑完——30 秒的逾時形同虛設。
//
// 實際踩到過：agent 一句 `find /` 讓任務整整卡了 5 分鐘，直到像素辦公室的 watchdog 把它
// 判定成「失聯」收卡。使用者看到的是任務莫名中斷，跟逾時該有的樣子完全不同。
//
// 逾時時仍回傳【已經收到的輸出】：對模型來說「跑到一半的結果 + 逾時」遠比一片空白有用。
// Detach 讓命令自成 process group，之後才能用 KillTree 收掉整棵子孫樹。
// 給【自己接管 Start/Wait】的呼叫端用（背景任務 TaskManager）：同步路徑走 runCombined 已經做了。
// 必須在 Start 之前呼叫。
func Detach(cmd *exec.Cmd) { setPgid(cmd) }

// KillTree 殺掉整個 process group。只有先 Detach 過才殺得到孫行程——否則 dev server 那類
// 「bash 起一個真正的行程」會在 bash 被殺後繼續活著，介面卻回報「已終止」。
func KillTree(cmd *exec.Cmd) { killGroup(cmd) }

func runCombined(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	setPgid(cmd)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return buf.Bytes(), err
	case <-ctx.Done():
		killGroup(cmd)
		<-done // 收屍。管線的寫入端全死了，Wait 立刻回來——讀 buf 也才沒有 data race
		return buf.Bytes(), ctx.Err()
	}
}
