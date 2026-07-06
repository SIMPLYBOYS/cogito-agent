package tools

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// mergeMu 序列化 worktree diff 的回寫——並行隔離 agent 各自在自己的 worktree 寫，完事一個一個 apply 回
// 主工作區，杜絕同時寫的競態。ponytail: 全域鎖（跨 session over-serialize apply），量大再改 per-repo 鎖。
var mergeMu sync.Mutex

// isGitRepo 判斷 dir 是否在 git 工作區內。
func isGitRepo(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// addWorktree 在 base repo 的 HEAD 開一個 detached worktree（獨立臨時目錄），回傳路徑與清理函式。
// base 非 git repo 則回錯（呼叫端據此降級為共享工作區）。
func addWorktree(base string) (string, func(), error) {
	if !isGitRepo(base) {
		return "", nil, fmt.Errorf("%s 不是 git 工作區", base)
	}
	dir, err := os.MkdirTemp("", "claw-wt-")
	if err != nil {
		return "", nil, err
	}
	if out, aerr := exec.Command("git", "-C", base, "worktree", "add", "--detach", dir, "HEAD").CombinedOutput(); aerr != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("git worktree add 失敗: %v: %s", aerr, strings.TrimSpace(string(out)))
	}
	cleanup := func() {
		_ = exec.Command("git", "-C", base, "worktree", "remove", "--force", dir).Run()
		_ = os.RemoveAll(dir)
	}
	return dir, cleanup, nil
}

// worktreeDiff 抓 worktree 相對 HEAD 的所有改動（含新檔）成可 apply 的 patch。
func worktreeDiff(wt string) (string, error) {
	if err := exec.Command("git", "-C", wt, "add", "-A").Run(); err != nil {
		return "", fmt.Errorf("git add 失敗: %w", err)
	}
	out, err := exec.Command("git", "-C", wt, "diff", "--cached").Output()
	if err != nil {
		return "", fmt.Errorf("git diff 失敗: %w", err)
	}
	return string(out), nil
}

// applyPatchToBase 把 patch 序列化 apply 回主工作區（並行隔離 agent 的回寫一個一個來）。空 patch 為 no-op。
// 用 --3way 讓不重疊的改動能各自併入；重疊時回錯，由呼叫端把 diff 附在報告裡交主 agent 處理。
func applyPatchToBase(base, patch string) error {
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	mergeMu.Lock()
	defer mergeMu.Unlock()
	cmd := exec.Command("git", "-C", base, "apply", "--3way", "--whitespace=nowarn")
	cmd.Stdin = strings.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply 回主工作區失敗（可能與其他改動衝突）: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
