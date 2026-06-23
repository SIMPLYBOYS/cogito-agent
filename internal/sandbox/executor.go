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
//
// Command 回傳一個【未 Start】的 *exec.Cmd（已配置成在此沙箱執行 command）：供背景任務（TaskManager）
// 自行接管 stdout/stderr、Start、Wait、Kill；同步 Run 也以它為基礎，確保兩條路徑隔離邊界一致。
type Executor interface {
	Run(ctx context.Context, command, workDir string) (output []byte, err error)
	Command(ctx context.Context, command, workDir string) (*exec.Cmd, error)
	Name() string // "host" / "docker"，供日誌與識別
}

// HostExecutor 在宿主機直接執行（原 bash 工具行為，零隔離）。預設值；mac 開發/單測不需任何外部依賴。
type HostExecutor struct{}

func (HostExecutor) Name() string { return "host" }

func (HostExecutor) Command(ctx context.Context, command, workDir string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = workDir
	return cmd, nil
}

func (h HostExecutor) Run(ctx context.Context, command, workDir string) ([]byte, error) {
	cmd, err := h.Command(ctx, command, workDir)
	if err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}
