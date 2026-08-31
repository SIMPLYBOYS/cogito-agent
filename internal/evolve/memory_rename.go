package evolve

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

// RenamePlan 是一筆記錄的標題改寫計畫。Slug 是檔名（正典 ID），改寫【不動】它。
type RenamePlan struct {
	Slug string
	Old  string
	New  string
}

var nameLineRe = regexp.MustCompile(`(?m)^name:.*$`)

// PlanRecordNames 掃記憶庫，算出哪些記錄的 frontmatter name 該改寫。
//
// 【為何需要】舊記錄的 name 是 learning 硬切前 24 字的產物，全斷在句子中間
// （見 docs/kg-status.md §4）。name 同時是知識圖譜節點 ID 與 [[link]] 目標，
// 斷句的名字沒有人寫得出指向它的連結。memoryTitle 已修好【產生端】，這裡補既有資料。
//
// 新標題由 Description（完整 learning）重算，不是從舊的斷句再切——從殘缺的東西再切
// 只會得到另一個殘缺的東西。
func PlanRecordNames(root string) []RenamePlan {
	loader := ctxpkg.NewMemoryLoaderAt(filepath.Join(root, ".claw", "memory"))
	var plans []RenamePlan
	for _, r := range loader.List() {
		src := r.Description
		if strings.TrimSpace(src) == "" {
			src = r.Body // 極舊的記錄可能沒 description，退回正文
		}
		want := memoryTitle(src)
		if want == "" || want == r.Name {
			continue
		}
		plans = append(plans, RenamePlan{
			Slug: strings.TrimSuffix(filepath.Base(r.Path), ".md"),
			Old:  r.Name,
			New:  want,
		})
	}
	return plans
}

// ApplyRecordNames 執行改寫。動的是【使用者資料】而且記憶庫通常不在版控裡，
// 所以先把整個 memory/ 複製到 .claw/memory-backup-<時間>/ 再動——回滾就是把它複製回去。
//
// 只改 frontmatter 的 name 那一行：檔名（內容定址的正典 ID）與正文一律不動，
// 整併的 UPDATE/DELETE 與撤回窗都靠檔名比對，改了會全部對不上。
func ApplyRecordNames(root string, plans []RenamePlan) (backupDir string, err error) {
	if len(plans) == 0 {
		return "", nil
	}
	memDir := filepath.Join(root, ".claw", "memory")
	backupDir = filepath.Join(root, ".claw", "memory-backup-"+time.Now().Format("20060102-150405"))
	if err := copyDirFlat(memDir, backupDir); err != nil {
		return "", fmt.Errorf("備份失敗（未改動任何檔案）: %w", err)
	}
	for _, p := range plans {
		path := filepath.Join(memDir, p.Slug+".md")
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return backupDir, fmt.Errorf("讀取 %s 失敗: %w", p.Slug, rerr)
		}
		out := nameLineRe.ReplaceAllLiteralString(string(raw), "name: "+p.New)
		if out == string(raw) {
			continue // 沒有 name 行可換，跳過而不是寫回原樣
		}
		if werr := os.WriteFile(path, []byte(out), 0o644); werr != nil {
			return backupDir, fmt.Errorf("寫入 %s 失敗: %w", p.Slug, werr)
		}
	}
	return backupDir, nil
}

// copyDirFlat 把目錄裡的檔案（單層）複製到 dst。
func copyDirFlat(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(src, e.Name()))
		if rerr != nil {
			return rerr
		}
		if werr := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); werr != nil {
			return werr
		}
	}
	return nil
}
