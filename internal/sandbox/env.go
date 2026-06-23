package sandbox

import (
	"fmt"
	"os"
)

// FromEnv 依環境變數選 Executor：
//
//	COGITO_SANDBOX=docker → DockerExecutor（OS 級硬隔離）
//	其他/未設            → HostExecutor（宿主機直跑，無硬隔離）
//
// Docker 模式可調：COGITO_SANDBOX_IMAGE / _MEMORY / _CPUS / _NETWORK / _PIDS。
func FromEnv() Executor {
	if os.Getenv("COGITO_SANDBOX") != "docker" {
		return HostExecutor{}
	}
	return NewDockerExecutor(DockerConfig{
		Image:   os.Getenv("COGITO_SANDBOX_IMAGE"),
		Memory:  envOr("COGITO_SANDBOX_MEMORY", "512m"),
		CPUs:    envOr("COGITO_SANDBOX_CPUS", "1.0"),
		Network: os.Getenv("COGITO_SANDBOX_NETWORK"),
		Pids:    envOr("COGITO_SANDBOX_PIDS", "256"),
	})
}

// Describe 回傳一行人類可讀的生效設定，供啟動日誌。
func Describe(ex Executor) string {
	if d, ok := ex.(DockerExecutor); ok {
		c := d.Config()
		return fmt.Sprintf("docker（image=%s, network=%s, memory=%s, cpus=%s, pids=%s）—— OS 級硬隔離",
			c.Image, c.Network, c.Memory, c.CPUs, c.Pids)
	}
	return "host（宿主機直跑，無硬隔離；僅靠黑名單/審批/路徑分目錄等軟性防線）"
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
