package evolve

// 記憶庫進 git 的合約：一提案一 commit（revert 即回滾單條）、撤回也留帳、
// root 不是 git 工作區時靜默降級（git 是加值層，不是前提）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitMem(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initMemRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitMem(t, root, "init", "-q")
	gitMem(t, root, "config", "user.email", "test@test")
	gitMem(t, root, "config", "user.name", "test")
	if err := os.MkdirAll(filepath.Join(root, ".claw"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// proposalOf 組最小提案檔內容（writeProposal 是 reconcile 測試裡現成的，吃整段 body）。
func proposalOf(bullets ...string) string {
	b := "## [慣例] 來自任務「測試」（2026-09-01）\n"
	for _, l := range bullets {
		b += "- " + l + "\n"
	}
	return b
}

func TestApplyProposedMemoryCommitsPerEntry(t *testing.T) {
	root := initMemRepo(t)
	writeProposal(t, root, proposalOf("甲條學習", "乙條學習"))
	applied, _, err := ApplyProposedMemory(root)
	if err != nil || len(applied) != 2 {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	subjects := strings.Split(gitMem(t, root, "log", "--format=%s"), "\n")
	if len(subjects) != 2 { // 一提案一 commit：兩條放行＝恰好兩個 commit，不多不少
		t.Fatalf("要 2 個 commit，得到 %d：%v", len(subjects), subjects)
	}
	for _, s := range subjects {
		if !strings.HasPrefix(s, "memory: 放行 ") {
			t.Fatalf("commit 主旨不對: %q", s)
		}
	}
	if files := gitMem(t, root, "ls-files", ".claw/memory"); len(strings.Split(files, "\n")) != 2 {
		t.Fatalf("記錄檔沒被追蹤: %q", files)
	}
}

func TestRevokeAutopassCommits(t *testing.T) {
	root := initMemRepo(t)
	writeProposal(t, root, proposalOf("丙條學習"))
	if _, _, err := ApplyProposedMemory(root); err != nil {
		t.Fatal(err)
	}
	saveAutopass(root, []AutopassEntry{{File: memSlug("丙條學習") + ".md", Desc: "丙條學習", At: time.Now()}})
	if _, err := RevokeAutopass(root, 1); err != nil {
		t.Fatal(err)
	}
	if s := gitMem(t, root, "log", "-1", "--format=%s"); !strings.HasPrefix(s, "memory: 撤回 ") {
		t.Fatalf("撤回沒留 commit: %q", s)
	}
	// 撤回的 commit 要記錄「移去歸檔」而不是憑空消失
	if files := gitMem(t, root, "ls-files", ".claw/memory-archive"); files == "" {
		t.Fatal("歸檔檔沒被追蹤")
	}
}

func TestMemoryGitDegradesWithoutRepo(t *testing.T) {
	root := t.TempDir() // 沒有 git init——放行必須照常成功
	if err := os.MkdirAll(filepath.Join(root, ".claw"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProposal(t, root, proposalOf("丁條學習"))
	applied, _, err := ApplyProposedMemory(root)
	if err != nil || len(applied) != 1 {
		t.Fatalf("非 git 工作區放行失敗: applied=%v err=%v", applied, err)
	}
}
