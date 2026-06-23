package sandbox

import (
	"context"
	"os/exec"
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

// DockerExecutor 把每條命令丟進一個一次性容器執行（docker run --rm）：
//   - 只把 workDir 以 volume 掛入容器的 Mount 點 → 宿主機其餘檔案系統不可見，`cd /` 也逃不出去；
//   - --network none 預設斷網；--memory/--cpus/--pids-limit 限資源（擋 fork bomb / 吃爆記憶體）。
//
// 語義對齊 HostExecutor：每次呼叫是全新環境，檔案透過掛載的 workDir 持久化（與宿主機「每次新 shell、
// 磁碟保留」一致）。容器內安裝的套件/背景進程不跨呼叫保留——若需持久 env，未來可升級成
// 「per-session 常駐容器 + docker exec」。
type DockerExecutor struct {
	cfg DockerConfig
}

func NewDockerExecutor(cfg DockerConfig) DockerExecutor {
	return DockerExecutor{cfg: cfg.withDefaults()}
}

func (d DockerExecutor) Name() string { return "docker" }

// Config 回傳生效中的設定（含預設），供啟動時日誌顯示。
func (d DockerExecutor) Config() DockerConfig { return d.cfg }

func (d DockerExecutor) Run(ctx context.Context, command, workDir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", d.dockerArgs(command, workDir)...)
	return cmd.CombinedOutput()
}

// dockerArgs 組裝 docker run 參數（抽出以便單測，不需真的有 Docker daemon）。
func (d DockerExecutor) dockerArgs(command, workDir string) []string {
	args := []string{
		"run", "--rm",
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
	return append(args, d.cfg.Image, "bash", "-c", command)
}
