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

func TestDockerExecutor_RunArgsIsolation(t *testing.T) {
	d := NewDockerExecutor(DockerConfig{Memory: "512m", CPUs: "1.0", Pids: "256"})
	s := argString(d.runArgs("cogito-sb-abc", "/abs/work"))

	for _, want := range []string{
		"run -d --name cogito-sb-abc", // 常駐（非 --rm）
		"-v /abs/work:/workspace",     // 只掛 workDir → 宿主機其餘不可見
		"-w /workspace",
		"--network none", // 斷網
		"--memory 512m",
		"--cpus 1.0",
		"--pids-limit 256",
		"cogito-sandbox:latest sleep infinity", // 常駐等待 exec
	} {
		if !strings.Contains(s, want) {
			t.Errorf("docker run 參數缺少 %q\n完整: %s", want, s)
		}
	}
}

func TestDockerExecutor_ExecArgs(t *testing.T) {
	d := NewDockerExecutor(DockerConfig{})
	args := d.execArgs("cogito-sb-abc", "go build ./...")
	s := argString(args)
	if !strings.Contains(s, "exec -w /workspace cogito-sb-abc bash -c") {
		t.Errorf("exec 參數不對: %s", s)
	}
	if args[len(args)-1] != "go build ./..." {
		t.Errorf("命令應為最後一個參數，got %q", args[len(args)-1])
	}
}

func TestDockerExecutor_OmitsEmptyLimits(t *testing.T) {
	d := NewDockerExecutor(DockerConfig{}) // 無 Memory/CPUs/Pids
	s := argString(d.runArgs("n", "/w"))
	for _, unwanted := range []string{"--memory", "--cpus", "--pids-limit"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("未設限制時不應出現 %q: %s", unwanted, s)
		}
	}
}

func TestContainerName_Stable(t *testing.T) {
	a1 := containerName("/path/one")
	a2 := containerName("/path/one")
	b := containerName("/path/two")
	if a1 != a2 {
		t.Errorf("同 workDir 應得同容器名: %s vs %s", a1, a2)
	}
	if a1 == b {
		t.Error("不同 workDir 應得不同容器名")
	}
	if !strings.HasPrefix(a1, "cogito-sb-") {
		t.Errorf("容器名前綴不對: %s", a1)
	}
}
