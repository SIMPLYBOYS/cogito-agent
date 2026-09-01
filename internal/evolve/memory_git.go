package evolve

// 記憶庫進 git（memory-review 會議 rev_rollback 卡的落地；munder-difflin 對照筆記 ①）。
//
// 撤回窗（memory_autopass.go）只保護 72 小時內的自動放行；過窗之後、以及人工 `apply memory`
// 放行的，錯了只能手動刪檔。git 補上這一半：每放行一條 commit 一條（爆炸半徑鎖在單條，
// revert 即回滾）、撤回也留 commit（帳目完整）。
//
// 單一提交者：只有這裡 commit 記憶——agent 的 git 工具動的是工作區的程式碼，不動記憶目錄。
// root 不在 git 工作區＝靜默跳過（與 worktree 隔離同款降級：git 是加值層，不是前提）。
// git 失敗也只記 log 不回錯——記憶操作的成敗不能被 git 綁架（記錄檔本身已落盤）。

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// memoryGitPaths 是 commit 的 pathspec 白名單：只碰記憶本體與歸檔，不掃進 agent 可能
// staged 到 index 的其他東西（pathspec 限定的 commit 不動白名單外的 staged 內容）。
var memoryGitPaths = []string{".claw/memory", ".claw/memory-archive"}

// commitMemory 把記憶目錄的當前變化提交進 root 所在的 git repo。訊息單行、截短到主旨長度。
func commitMemory(root, msg string) {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return
	}
	var paths []string
	for _, d := range memoryGitPaths {
		if st, err := os.Stat(filepath.Join(root, d)); err == nil && st.IsDir() {
			paths = append(paths, d)
		}
	}
	if len(paths) == 0 {
		return
	}
	if err := exec.Command("git", append([]string{"-C", root, "add", "-A", "--"}, paths...)...).Run(); err != nil {
		log.Printf("[evolve] 記憶 git add 失敗（略過，記錄本身已落盤）: %v", err)
		return
	}
	// 沒變化就不留空 commit。用 status 而非 diff --cached：後者在還沒有首個 commit 的
	// repo（unborn HEAD）行為不一。
	st, err := exec.Command("git", append([]string{"-C", root, "status", "--porcelain", "--"}, paths...)...).Output()
	if err != nil || strings.TrimSpace(string(st)) == "" {
		return
	}
	msg = "memory: " + schema.TruncRunes(oneLine(msg), 72, "…")
	if err := exec.Command("git", append([]string{"-C", root, "commit", "--no-verify", "-m", msg, "--"}, paths...)...).Run(); err != nil {
		log.Printf("[evolve] 記憶 git commit 失敗（略過，記錄本身已落盤）: %v", err)
	}
}
