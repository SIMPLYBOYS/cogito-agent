package sandbox

import (
	"strings"
	"testing"
)

// argString 把 args 串成一條好 grep 的字串（含成對 flag）。
func argString(args []string) string { return strings.Join(args, " ") }

func TestDockerExecutor_Defaults(t *testing.T) {
	d := NewDockerExecutor(DockerConfig{})
	cfg := d.Config()
	if cfg.Image != "cogito-sandbox:latest" || cfg.Mount != "/workspace" || cfg.Network != "none" {
		t.Errorf("預設值不對: %+v", cfg)
	}
	if d.Name() != "docker" {
		t.Errorf("Name 應為 docker，got %q", d.Name())
	}
}

func TestDockerExecutor_ArgsIsolation(t *testing.T) {
	d := NewDockerExecutor(DockerConfig{Memory: "512m", CPUs: "1.0", Pids: "256"})
	args := d.dockerArgs("go build ./...", "/abs/work")
	s := argString(args)

	// 硬邊界要素都在
	for _, want := range []string{
		"run --rm",
		"-v /abs/work:/workspace", // 只掛 workDir → 宿主機其餘不可見
		"-w /workspace",
		"--network none", // 斷網
		"--memory 512m",
		"--cpus 1.0",
		"--pids-limit 256",
		"cogito-sandbox:latest bash -c",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("docker 參數缺少 %q\n完整: %s", want, s)
		}
	}
	// 命令是最後一個參數，原樣傳入
	if args[len(args)-1] != "go build ./..." {
		t.Errorf("命令應為最後一個參數，got %q", args[len(args)-1])
	}
}

func TestDockerExecutor_OmitsEmptyLimits(t *testing.T) {
	d := NewDockerExecutor(DockerConfig{}) // 無 Memory/CPUs/Pids
	s := argString(d.dockerArgs("ls", "/w"))
	for _, unwanted := range []string{"--memory", "--cpus", "--pids-limit"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("未設限制時不應出現 %q: %s", unwanted, s)
		}
	}
}
