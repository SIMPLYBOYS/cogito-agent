package sandbox

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// captureLog 攔截 WarnIfHost 的輸出（它走標準 log）。
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	fn()
	return buf.String()
}

func TestWarnIfHost_WarnsOnHost(t *testing.T) {
	out := captureLog(t, func() { WarnIfHost(HostExecutor{}) })
	if !strings.Contains(out, "安全警告") {
		t.Error("host 模式應打安全警告")
	}
	if !strings.Contains(out, "COGITO_SANDBOX=docker") {
		t.Error("警告應指出修法（COGITO_SANDBOX=docker）")
	}
}

func TestWarnIfHost_SilentOnDocker(t *testing.T) {
	out := captureLog(t, func() { WarnIfHost(NewDockerExecutor(DockerConfig{})) })
	if out != "" {
		t.Errorf("docker 模式不該有警告，got: %q", out)
	}
}
