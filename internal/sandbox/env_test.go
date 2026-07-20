package sandbox

import (
	"strings"
	"testing"
)

// FromEnv 決定「有沒有 OS 級硬隔離」——是安全相關路徑，須測分歧。
func TestFromEnv_DockerVsHost(t *testing.T) {
	t.Setenv("COGITO_SANDBOX", "docker")
	if _, ok := FromEnv().(*DockerExecutor); !ok {
		t.Error("COGITO_SANDBOX=docker 應回 DockerExecutor")
	}

	t.Setenv("COGITO_SANDBOX", "") // 未設/其他 → host 直跑
	if _, ok := FromEnv().(HostExecutor); !ok {
		t.Error("未設 COGITO_SANDBOX 應回 HostExecutor（宿主機直跑）")
	}
}

// Docker 模式的資源旋鈕走 envOr 預設；設了就用設定值。
func TestFromEnv_DockerDefaultsAndOverride(t *testing.T) {
	t.Setenv("COGITO_SANDBOX", "docker")
	t.Setenv("COGITO_SANDBOX_MEMORY", "")  // 用預設
	t.Setenv("COGITO_SANDBOX_CPUS", "2.5") // 覆蓋
	d, ok := FromEnv().(*DockerExecutor)
	if !ok {
		t.Fatal("應為 DockerExecutor")
	}
	c := d.Config()
	if c.Memory != "512m" {
		t.Errorf("memory 預設應為 512m，got %q", c.Memory)
	}
	if c.CPUs != "2.5" {
		t.Errorf("cpus 應取環境覆蓋值 2.5，got %q", c.CPUs)
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("X_ENVOR_TEST", "")
	if envOr("X_ENVOR_TEST", "def") != "def" {
		t.Error("未設應回預設")
	}
	t.Setenv("X_ENVOR_TEST", "v")
	if envOr("X_ENVOR_TEST", "def") != "v" {
		t.Error("有設應回設定值")
	}
}

// Describe 的兩個型別分支（docker / host）都要能產出正確描述。
func TestDescribe_DockerAndHost(t *testing.T) {
	host := Describe(HostExecutor{})
	if !strings.Contains(host, "host") || !strings.Contains(host, "無硬隔離") {
		t.Errorf("host 描述錯誤: %q", host)
	}
	dk := Describe(NewDockerExecutor(DockerConfig{Image: "img", Network: "none", Memory: "512m", CPUs: "1.0", Pids: "256"}))
	if !strings.Contains(dk, "docker") || !strings.Contains(dk, "硬隔離") {
		t.Errorf("docker 描述錯誤: %q", dk)
	}
}
