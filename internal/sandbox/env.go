package sandbox

import (
	"fmt"
	"log"
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

// WarnIfHost 供【開放入口】的服務（bot）啟動時呼叫：host 模式下 bash 直接在宿主機以本行程權限執行，
// 而 bot 的 prompt 來自聊天平台——「陌生 prompt → bash → 宿主機」就是一條 RCE 路徑。入口白名單擋得住
// 陌生人，但擋不住 prompt injection（agent 讀到的網頁/MCP 資料裡藏指令，不是人發的）；黑名單是軟性的
// （繞過寫法很多），workDir 對 bash 也只是慣例不是邊界（cd .. 就出去了）。
//
// 只警告不阻擋——受控環境（內網、單人、已知 prompt）仍可能是合理選擇，決定權在營運者。
// claw-cli（使用者自己在終端下 prompt，信任模型等同自己打指令）不呼叫這個。
func WarnIfHost(ex Executor) {
	if _, ok := ex.(HostExecutor); !ok {
		return
	}
	log.Print("\n" +
		"╔══════════════════════════════════════════════════════════════════════════╗\n" +
		"║ ⚠️  安全警告：bash 正在【宿主機】直跑，無 OS 級隔離                        ║\n" +
		"║                                                                          ║\n" +
		"║ bot 是開放入口：聊天訊息會變成 bash 命令，在宿主機上以本行程的權限執行。  ║\n" +
		"║ 入口白名單擋得住陌生人，但擋不住 prompt injection（agent 讀到的網頁 /     ║\n" +
		"║ MCP 資料裡藏的指令）；黑名單與 workDir 對 bash 都只是軟性防線。           ║\n" +
		"║                                                                          ║\n" +
		"║ 正式上線請開硬隔離：  COGITO_SANDBOX=docker                               ║\n" +
		"║ （bash 進只掛 workspace 的容器，預設斷網 + 限記憶體/CPU/PID）             ║\n" +
		"╚══════════════════════════════════════════════════════════════════════════╝")
}

// Describe 回傳一行人類可讀的生效設定，供啟動日誌。
func Describe(ex Executor) string {
	if d, ok := ex.(*DockerExecutor); ok {
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
