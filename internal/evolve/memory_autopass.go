package evolve

// 自動放行的判準與撤回窗（memory-review 會議 rev_autopass 卡的落地）。
//
// COGITO_MEMORY_AUTOAPPLY=1 原本【無條件】放行所有新增——那正是被會議點名的缺陷。
// 改成四條判準全中才自動過，其餘留給人審：
//
//	① 純風格/表達慣例，不改決策行為 —— LLM 判（StyleJudge）。判不動、出錯＝不過，
//	   寧可多留給人審（fail-closed）。
//	② 可逆 —— 只吃【新增】：一條一檔、撤回＝歸檔一個檔。刪改（UPDATE/DELETE）本來就
//	   永不自動放行，這條由 additionCandidates 的過濾承擔。
//	③ 範圍窄 —— 單行、不超過 autopassMaxRunes 字。長篇大論的「慣例」多半夾帶敘事，
//	   該給人看過。
//	④ 衝突偵測零命中 —— 碰既有記憶任一條的一律人審（ConflictHits，同 memory list 的
//	   紅字判準）。
//
// 放行的掛進 72 小時撤回窗（.claw/autopass.json）：`undo memory` 列出、`undo memory <n>`
// 一鍵撤回（歸檔到 .claw/memory-archive/，可復原）。窗過即從帳上消失——塵埃落定。
//
// 【與卡片原文的一個偏離】卡片寫「過窗才生效」，但同場會議的前提卡裁定「衝突時往不空轉倒：
// 放行錯的可回滾止血、不放行的空轉無藥可醫」。四條全中的提案再壓 72 小時不生效，只剩空轉
// 沒有保護——所以做成【立即生效＋72 小時可撤回】，把窗用在止血而不是把關。

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

const (
	AutopassWindow   = 72 * time.Hour
	autopassMaxRunes = 100
	autopassFileName = "autopass.json"
)

// StyleJudge 回答判準①：每一條 learning 是否「純風格/表達慣例、不改決策行為」。
// 一次判一批（省一輪一次 LLM 呼叫）；回 nil 或長度不符＝全部不過。
type StyleJudge func(learnings []string) []bool

// AutopassEntry 是撤回窗裡的一筆：記錄檔名（內容雜湊，可重算）、內容、放行時間。
type AutopassEntry struct {
	File string    `json:"file"`
	Desc string    `json:"desc"`
	At   time.Time `json:"at"`
}

func autopassPath(root string) string {
	return filepath.Join(root, ".claw", autopassFileName)
}

func loadAutopass(root string) []AutopassEntry {
	var out []AutopassEntry
	b, err := os.ReadFile(autopassPath(root))
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func saveAutopass(root string, entries []AutopassEntry) {
	if len(entries) == 0 {
		_ = os.Remove(autopassPath(root))
		return
	}
	b, _ := json.Marshal(entries)
	_ = os.WriteFile(autopassPath(root), b, 0o644)
}

// AutopassPending 回傳仍在撤回窗內的放行紀錄；過窗的順手從帳上剪掉（塵埃落定，
// 不需要背景排程——跟 ExpireDeferred 掛在 list 上同一個理由：讀之前一定會經過這裡）。
func AutopassPending(root string) []AutopassEntry {
	all := loadAutopass(root)
	var live []AutopassEntry
	for _, e := range all {
		if time.Since(e.At) < AutopassWindow {
			live = append(live, e)
		}
	}
	if len(live) != len(all) {
		saveAutopass(root, live)
	}
	return live
}

// RevokeAutopass 撤回窗內第 n 筆（1-based，對齊 AutopassPending 的列序）：
// 記錄檔歸檔到 .claw/memory-archive/（可復原，跟 Prune 同一個落點），帳上移除。
func RevokeAutopass(root string, n int) (string, error) {
	live := AutopassPending(root)
	if n < 1 || n > len(live) {
		return "", fmt.Errorf("撤回窗內只有 %d 筆（用 `undo memory` 看清單）", len(live))
	}
	e := live[n-1]
	if err := ctxpkg.NewMemoryLoader(root).ArchiveRecord(e.File); err != nil {
		return "", fmt.Errorf("歸檔失敗: %w", err)
	}
	saveAutopass(root, append(live[:n-1], live[n:]...))
	commitMemory(root, "撤回 "+e.Desc)
	return e.Desc, nil
}

// AutoApplyAdditions 在 COGITO_MEMORY_AUTOAPPLY=1 時，把提案檔裡【四條判準全中】的新增
// 放行成記錄並掛進撤回窗；其餘原地留給人審。回傳實際放行的條目。
//
// 敢自動放行，是因為記憶的爆炸半徑本來就小：一條一個檔（撤回＝歸檔一個檔）、System Prompt
// 只放索引一行（正文要被 recall 才載入）、Prune 會把最久未用的歸檔。判準把「小爆炸半徑」
// 再收斂成「幾乎沒有半徑」：純風格、範圍窄、跟既有記憶零衝突。
func AutoApplyAdditions(root string, judge StyleJudge) ([]string, error) {
	if os.Getenv(EnvAutoApply) != "1" {
		return nil, nil
	}
	cands := additionCandidates(root) // ②：非破壞性、非畫像
	var narrow []ProposedMemoryEntry
	for _, e := range cands {
		l := strings.TrimSpace(e.Learning)
		if !strings.Contains(l, "\n") && len([]rune(l)) <= autopassMaxRunes { // ③
			narrow = append(narrow, e)
		}
	}
	var clean []ProposedMemoryEntry
	for _, e := range narrow {
		if len(ConflictHits(root, e)) == 0 { // ④
			clean = append(clean, e)
		}
	}
	if len(clean) == 0 {
		if n := len(cands); n > 0 {
			log.Printf("[evolve] 自動放行判準全數未過，%d 條留給人審（apply memory）", n)
		}
		return nil, nil
	}
	texts := make([]string, len(clean))
	for i, e := range clean {
		texts[i] = e.Learning
	}
	var verdicts []bool
	if judge != nil {
		verdicts = judge(texts) // ①：fail-closed——nil / 長度不符在下面一律當否
	}
	var nums []int
	for i, e := range clean {
		if i < len(verdicts) && verdicts[i] {
			nums = append(nums, e.N)
		}
	}
	if len(nums) == 0 {
		log.Printf("[evolve] 自動放行判準全數未過（①風格判定），%d 條留給人審", len(cands))
		return nil, nil
	}
	applied, _, err := ApplyProposedMemory(root, nums...)
	if len(applied) > 0 {
		now := time.Now()
		ledger := loadAutopass(root)
		for _, desc := range applied {
			ledger = append(ledger, AutopassEntry{File: memSlug(desc) + ".md", Desc: desc, At: now})
		}
		saveAutopass(root, ledger)
		log.Printf("[evolve] ⚡ 自動放行 %d 條記憶（四判準全中；%d 條留給人審；72h 內可 undo memory 撤回）",
			len(applied), len(cands)-len(applied))
	}
	return applied, err
}
