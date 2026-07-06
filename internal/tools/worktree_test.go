package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInitRepo(t *testing.T, base string) {
	t.Helper()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", base}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git 不可用（%v）：%s", err, out)
		}
	}
	run("init", "-q")
	_ = os.WriteFile(filepath.Join(base, "a.txt"), []byte("hello\n"), 0o644)
	run("add", "-A")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init")
}

// worktree 隔離全鏈路：開 worktree → 在裡面改/新增檔 → 抓 diff → apply 回主工作區。
func TestWorktreeRoundTrip(t *testing.T) {
	base := t.TempDir()
	gitInitRepo(t, base)
	if !isGitRepo(base) {
		t.Fatal("初始化後應為 git repo")
	}

	wt, cleanup, err := addWorktree(base)
	if err != nil {
		t.Fatalf("addWorktree: %v", err)
	}
	defer cleanup()

	// 模擬子 agent 在隔離 worktree 寫入：新增 b.txt、改 a.txt
	_ = os.WriteFile(filepath.Join(wt, "b.txt"), []byte("new file\n"), 0o644)
	_ = os.WriteFile(filepath.Join(wt, "a.txt"), []byte("hello\nmore\n"), 0o644)

	// apply 前，base 不該有這些改動（證明隔離）
	if _, err := os.Stat(filepath.Join(base, "b.txt")); err == nil {
		t.Fatal("隔離期間 base 不該出現 b.txt")
	}

	patch, err := worktreeDiff(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "b.txt") || !strings.Contains(patch, "more") {
		t.Fatalf("patch 應含新檔與改動:\n%s", patch)
	}

	if err := applyPatchToBase(base, patch); err != nil {
		t.Fatalf("applyPatchToBase: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(base, "b.txt")); string(data) != "new file\n" {
		t.Errorf("apply 後 base 應有 b.txt，got %q", string(data))
	}
	if data, _ := os.ReadFile(filepath.Join(base, "a.txt")); !strings.Contains(string(data), "more") {
		t.Error("apply 後 a.txt 應被修改")
	}
}

func TestApplyPatch_EmptyNoop(t *testing.T) {
	if err := applyPatchToBase(t.TempDir(), "   \n"); err != nil {
		t.Errorf("空 patch 應 no-op，got %v", err)
	}
}

func TestAddWorktree_NonGitDegrades(t *testing.T) {
	if _, _, err := addWorktree(t.TempDir()); err == nil {
		t.Error("非 git 目錄應回錯（呼叫端據此降級為共享工作區）")
	}
}
