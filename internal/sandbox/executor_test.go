package sandbox

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostExecutor_Run(t *testing.T) {
	out, err := HostExecutor{}.Run(context.Background(), "echo hello", "")
	if err != nil {
		t.Fatalf("Run 失敗: %v", err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("應含 hello，got %q", out)
	}
}

func TestHostExecutor_WorkDir(t *testing.T) {
	dir := t.TempDir()
	out, err := HostExecutor{}.Run(context.Background(), "pwd", dir)
	if err != nil {
		t.Fatalf("Run 失敗: %v", err)
	}
	// mac 上 /var → /private/var 軟連結，pwd 路徑前綴會變，故只比對末段。
	if !strings.Contains(string(out), filepath.Base(dir)) {
		t.Errorf("工作目錄應為 %s，got %q", dir, out)
	}
}

func TestHostExecutor_Name(t *testing.T) {
	var ex Executor = HostExecutor{}
	if ex.Name() != "host" {
		t.Errorf("Name 應為 host，got %q", ex.Name())
	}
}
