package evolve

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

// 暫緩：審核的第三態。出自記憶審核那場會的 rev_defer_deadline 卡——
//
//   「垃圾堆的本質是【沒有到期日的暫存】，設死線就不會變成新的堆積。」
//
// 所以暫緩一定要帶【原因】與【復審期限】，而且【超時自動降級為否決】。沒有死線的暫緩
// 只是把「還沒審」換個名字，那正是眼前這 31 條的成因。
//
// 摩擦刻意不對稱：暫緩強制寫原因，放行與否決不用寫。理由是只有暫緩會把東西留在佇列裡，
// 而留下來的東西必須有人能看懂當初為什麼留。

const deferFileName = "memory-deferred.json"

// DefaultDeferDays 是沒指定時的復審期限。給預設值不是偷懶——強迫每次都想「幾天」
// 會讓人乾脆不用暫緩，那就退回二選一了；重點是【有】死線，不是那個數字多精準。
const DefaultDeferDays = 7

type deferred struct {
	Why  string    `json:"why"`
	Due  time.Time `json:"due"`
	Text string    `json:"text"` // 存原文供人看；比對仍走 key
}

// deferKey 用【內容】當鍵，不用編號。編號會隨著別條被放行/否決而位移——
// 拿它當鍵的話，暫緩過的東西會在下一次操作後對到別條身上。
func deferKey(e ProposedMemoryEntry) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(e.Op + "|" + e.Target + "|" + e.Learning))
	return fmt.Sprintf("%08x", h.Sum32())
}

func deferPath(root string) string { return filepath.Join(root, ".claw", deferFileName) }

func loadDeferred(root string) map[string]deferred {
	out := map[string]deferred{}
	if data, err := os.ReadFile(deferPath(root)); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func saveDeferred(root string, m map[string]deferred) error {
	data, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(deferPath(root), data, 0o644)
}

// DeferProposal 把一條提案標成暫緩。原因是必填——留在佇列裡的東西必須有人看得懂為什麼。
func DeferProposal(root string, e ProposedMemoryEntry, days int, why string) error {
	if strings.TrimSpace(why) == "" {
		return fmt.Errorf("暫緩要寫一句原因（留在佇列裡的東西，之後要有人看得懂當初為什麼留）")
	}
	if days <= 0 {
		days = DefaultDeferDays
	}
	ctxpkg.LockKnowledge()
	defer ctxpkg.UnlockKnowledge()
	m := loadDeferred(root)
	m[deferKey(e)] = deferred{Why: strings.TrimSpace(why),
		Due: time.Now().AddDate(0, 0, days), Text: e.Learning}
	return saveDeferred(root, m)
}

// DeferredNote 回傳這條提案的暫緩註記（沒暫緩過就是空字串）。供清單顯示。
func DeferredNote(root string, e ProposedMemoryEntry) string {
	d, ok := loadDeferred(root)[deferKey(e)]
	if !ok {
		return ""
	}
	left := int(time.Until(d.Due).Hours() / 24)
	return fmt.Sprintf("暫緩中，還剩 %d 天（%s）", left, d.Why)
}

// ExpireDeferred 把過期的暫緩【降級為否決】——這是死線唯一有意義的地方。
// 回傳被降級的提案原文。呼叫端該在每次列清單前跑一次：沒有背景排程也不會忘記。
func ExpireDeferred(root string) []string {
	m := loadDeferred(root)
	if len(m) == 0 {
		return nil
	}
	now := time.Now()
	var overdue []string
	byKey := map[string]bool{}
	for k, d := range m {
		if now.After(d.Due) {
			overdue = append(overdue, d.Text)
			byKey[k] = true
		}
	}
	if len(overdue) == 0 {
		return nil
	}
	// 對得上編號才能否決：提案檔的順序會變，所以每次重新解析、重新對鍵。
	var nums []int
	for _, e := range ListProposedMemory(root) {
		if byKey[deferKey(e)] {
			nums = append(nums, e.N)
		}
	}
	if len(nums) > 0 {
		_, _ = DiscardProposedMemory(root, nums...)
	}
	ctxpkg.LockKnowledge()
	defer ctxpkg.UnlockKnowledge()
	m = loadDeferred(root)
	for k := range byKey {
		delete(m, k)
	}
	_ = saveDeferred(root, m)
	return overdue
}
