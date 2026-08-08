package sandbox

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// 逾時必須殺掉【孫行程】，否則它握著 stdout 管線，呼叫端會一路等到它自己跑完——
// 30 秒的 bash 逾時就是這樣被一句 `find /` 破功，任務卡滿 5 分鐘被判失聯。
// `sleep 10 & wait` 刻意讓 sleep 成為孫子（bash 不會把它 exec 掉取代自己）。
func TestHostExecutor_TimeoutKillsGrandchild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := HostExecutor{}.Run(ctx, "echo 開始; sleep 10 & wait", t.TempDir())
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("應回報逾時，got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("逾時後仍被孫行程卡住 %v（修好的話應該幾百毫秒就回來）", elapsed)
	}
}

func TestHostExecutor_TimeoutKeepsPartialOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	// 逾時也要把已經拿到的輸出交出去：「跑到一半的結果 + 逾時」比一片空白有用。
	out, _ := HostExecutor{}.Run(ctx, "echo 前半段; sleep 10 & wait", t.TempDir())
	if !strings.Contains(string(out), "前半段") {
		t.Errorf("逾時前的輸出不該被丟掉，got %q", out)
	}
}

func TestHostExecutor_Name(t *testing.T) {
	var ex Executor = HostExecutor{}
	if ex.Name() != "host" {
		t.Errorf("Name 應為 host，got %q", ex.Name())
	}
}
