// Package sandbox 把「命令在哪裡執行」這件事抽象成 Executor，讓 bash 工具不再寫死在宿主機 exec。
//
// 預設 HostExecutor 維持原行為（宿主機跑 bash -c）；DockerExecutor 把命令丟進隔離容器，
// 提供整個專案缺的【OS 級硬邊界】——這是「失控控制」主題裡軟性防線（黑名單/審批/路徑分目錄）
// 之外，唯一能真正擋住 `cd /` 逃逸、限網、限資源的一層。
package sandbox

import (
	"context"
	"os/exec"
)

// Executor 執行一條 shell 命令並回傳合併的 stdout+stderr。
//
// 約定：非零退出等「命令層級錯誤」以 error 回傳（呼叫方——bash 工具——走 error-as-observation
// 把它轉成給模型看的觀察，而非中斷迴圈）。ctx 帶 timeout，實作必須尊重取消。
type Executor interface {
	Run(ctx context.Context, command, workDir string) (output []byte, err error)
	Name() string // "host" / "docker"，供日誌與識別
}

// HostExecutor 在宿主機直接執行（原 bash 工具行為，零隔離）。預設值；mac 開發/單測不需任何外部依賴。
type HostExecutor struct{}

func (HostExecutor) Name() string { return "host" }

func (HostExecutor) Run(ctx context.Context, command, workDir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = workDir
	return cmd.CombinedOutput()
}
