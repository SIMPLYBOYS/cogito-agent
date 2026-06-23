package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// fakeExecutor 捕獲傳給它的命令/工作目錄，並回傳預設輸出，驗證 bash 工具確實走 Executor。
type fakeExecutor struct {
	gotCmd     string
	gotWorkDir string
	out        []byte
	err        error
}

func (f *fakeExecutor) Name() string { return "fake" }

func (f *fakeExecutor) Run(_ context.Context, command, workDir string) ([]byte, error) {
	f.gotCmd = command
	f.gotWorkDir = workDir
	return f.out, f.err
}

func (f *fakeExecutor) Command(ctx context.Context, command, workDir string) (*exec.Cmd, error) {
	f.gotCmd = command
	f.gotWorkDir = workDir
	return exec.CommandContext(ctx, "true"), nil
}

func TestBashTool_RoutesThroughExecutor(t *testing.T) {
	fe := &fakeExecutor{out: []byte("hi")}
	bt := NewBashToolWithExecutor("/wd", fe)

	out, err := bt.Execute(context.Background(), []byte(`{"command":"ls -la"}`))
	if err != nil {
		t.Fatalf("Execute 失敗: %v", err)
	}
	if fe.gotCmd != "ls -la" || fe.gotWorkDir != "/wd" {
		t.Errorf("命令/工作目錄未正確傳給 executor: cmd=%q dir=%q", fe.gotCmd, fe.gotWorkDir)
	}
	if out != "hi" {
		t.Errorf("應回傳 executor 的輸出，got %q", out)
	}
}

func TestBashTool_ErrorAsObservation(t *testing.T) {
	fe := &fakeExecutor{out: []byte("boom"), err: fmt.Errorf("exit status 1")}
	bt := NewBashToolWithExecutor("/wd", fe)

	out, err := bt.Execute(context.Background(), []byte(`{"command":"false"}`))
	if err != nil {
		t.Fatalf("命令失敗應為 error-as-observation，不回 Go error: %v", err)
	}
	if !strings.Contains(out, "執行報錯") || !strings.Contains(out, "boom") {
		t.Errorf("應把錯誤與輸出轉成觀察，got %q", out)
	}
}

func TestBashTool_NilExecutorFallsBackToHost(t *testing.T) {
	bt := NewBashToolWithExecutor(t.TempDir(), nil) // nil → HostExecutor
	out, err := bt.Execute(context.Background(), []byte(`{"command":"echo fallback"}`))
	if err != nil {
		t.Fatalf("Execute 失敗: %v", err)
	}
	if !strings.Contains(out, "fallback") {
		t.Errorf("nil executor 應退回 HostExecutor 實際執行，got %q", out)
	}
}
