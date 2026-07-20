package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"sync"
)

// DockerConfig 控制 DockerExecutor 的隔離與資源邊界。零值欄位會套用安全預設。
type DockerConfig struct {
	Image   string // 容器映像（預設 cogito-sandbox:latest）
	Mount   string // 容器內掛載點（預設 /workspace）
	Network string // --network（預設 none，完全斷網）
	Memory  string // --memory（如 512m，空則不限）
	CPUs    string // --cpus（如 1.0，空則不限）
	Pids    string // --pids-limit（如 256，空則不限；擋 fork bomb）
}

func (c DockerConfig) withDefaults() DockerConfig {
	if c.Image == "" {
		c.Image = "cogito-sandbox:latest"
	}
	if c.Mount == "" {
		c.Mount = "/workspace"
	}
	if c.Network == "" {
		c.Network = "none"
	}
	return c
}

// DockerExecutor 為每個 session（以 workDir 識別）維持一個【常駐容器】，命令經 docker exec 進去執行：
//   - 首次對某 workDir 執行時 docker run -d ... sleep infinity 拉起容器（只掛 workDir:/workspace →
//     宿主機其餘檔案系統不可見，cd / 逃不出去）；--network none 斷網、--memory/--cpus/--pids-limit 限資源；
//   - 之後同 workDir 的命令都走 docker exec，省去每命令啟動容器的延遲，且容器內安裝的套件 / 寫入的檔案 /
//     背景進程在同 session 多次呼叫間【持久保留】。注意：每條命令是獨立的 docker exec ... bash -c（全新
//     進程），故 shell 的 export 環境變數 / cd / 別名【不】跨呼叫保留——要持久得寫進檔案（如 ~/.bashrc）。
//
// 容器名由 workDir 雜湊決定（穩定、可在崩潰重啟後辨識並清理）。Close 會移除本進程拉起的所有容器。
type DockerExecutor struct {
	cfg     DockerConfig
	mu      sync.Mutex
	started map[string]string // workDir → 容器名
}

func NewDockerExecutor(cfg DockerConfig) *DockerExecutor {
	return &DockerExecutor{cfg: cfg.withDefaults(), started: make(map[string]string)}
}

func (d *DockerExecutor) Name() string { return "docker" }

// Config 回傳生效中的設定（含預設），供啟動時日誌顯示。
func (d *DockerExecutor) Config() DockerConfig { return d.cfg }

// containerName 由 workDir 推導穩定容器名（docker 名稱字元限制：用 hex 雜湊最安全）。
func containerName(workDir string) string {
	h := sha256.Sum256([]byte(workDir))
	return "cogito-sb-" + hex.EncodeToString(h[:])[:12]
}

// runArgs 組裝「拉起常駐容器」的 docker run 參數（抽出以便單測）。
func (d *DockerExecutor) runArgs(name, workDir string) []string {
	args := []string{
		"run", "-d", "--name", name,
		"-v", workDir + ":" + d.cfg.Mount,
		"-w", d.cfg.Mount,
		"--network", d.cfg.Network,
	}
	if d.cfg.Memory != "" {
		args = append(args, "--memory", d.cfg.Memory)
	}
	if d.cfg.CPUs != "" {
		args = append(args, "--cpus", d.cfg.CPUs)
	}
	if d.cfg.Pids != "" {
		args = append(args, "--pids-limit", d.cfg.Pids)
	}
	// sleep infinity 讓容器常駐等待 exec。
	return append(args, d.cfg.Image, "sleep", "infinity")
}

// execArgs 組裝「在常駐容器內執行命令」的 docker exec 參數（抽出以便單測）。
func (d *DockerExecutor) execArgs(name, command string) []string {
	return []string{"exec", "-w", d.cfg.Mount, name, "bash", "-c", command}
}

// ensure 確保該 workDir 的常駐容器已啟動，回傳容器名。對同一 workDir 只會建立一次。
func (d *DockerExecutor) ensure(ctx context.Context, workDir string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if name, ok := d.started[workDir]; ok {
		return name, nil // 快路徑：本 session 容器已在跑
	}

	name := containerName(workDir)
	// 清掉可能殘留的同名容器（上次進程崩潰留下的），再起全新常駐容器。best-effort。
	_ = exec.Command("docker", "rm", "-f", name).Run()

	if out, err := exec.CommandContext(ctx, "docker", d.runArgs(name, workDir)...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("啟動 sandbox 容器失敗（請確認 docker 可用且映像 %s 已建置）: %v\n%s", d.cfg.Image, err, out)
	}
	d.started[workDir] = name
	return name, nil
}

func (d *DockerExecutor) Command(ctx context.Context, command, workDir string) (*exec.Cmd, error) {
	name, err := d.ensure(ctx, workDir)
	if err != nil {
		return nil, err
	}
	// 這裡【不】過濾環境變數：繼承的是給 docker CLI 自己用的（DOCKER_HOST / DOCKER_CONTEXT 等），
	// 過濾掉反而會連不上 daemon。命令實際跑在容器裡，用的是【容器自己的】環境——我們沒帶 -e，
	// 宿主機的金鑰進不去，故本模式天生沒有 host 模式那個外洩面。
	return exec.CommandContext(ctx, "docker", d.execArgs(name, command)...), nil
}

func (d *DockerExecutor) Run(ctx context.Context, command, workDir string) ([]byte, error) {
	cmd, err := d.Command(ctx, command, workDir)
	if err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

// Close 移除本進程拉起的所有常駐容器（cmd 優雅關閉時呼叫）。
func (d *DockerExecutor) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for workDir, name := range d.started {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		delete(d.started, workDir)
	}
	return nil
}
