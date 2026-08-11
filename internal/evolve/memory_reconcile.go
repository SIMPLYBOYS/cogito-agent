package evolve

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// 記憶整併：把「舊記憶被新事實推翻」這件事變成【可 diff 的提案】。設計與理由見
// docs/memory-reconcile-format.md。
//
// 為何不叫 Consolidate：internal/tools/consolidate.go 已經佔用該名，而且做的是相反的事
// ——那支是「把當前工作沉澱成【新】提案」，這裡是「整理【既有】記憶」。同名會讓人分不清。
//
// ponytail: 模型介面走 JSON 而非 qm 的行文法（`UPDATE <n>: ...`）。四個動作語意完全一致，
// 但 JSON 能複用既有的 extractJSON + Unmarshal，且事實內容含冒號時不會把解析弄壞。
// 檔案格式仍是人類可讀的動作 bullet——那是給人審的，兩者不必同一套。

// ReconcileKind 是整併提案的分類標記，會寫進提案區塊標頭與放行後記錄的 tags。
const ReconcileKind = "整併"

// maxReconcileRecords 是一次整併最多看幾筆記錄。整份記憶庫要進 prompt，太多會降低模型
// 逐條比對的品質（成本反而不是問題：60 條描述對便宜模型不到一分錢）。挑哪幾條見 pickForReconcile。
const maxReconcileRecords = 60

const reconcileSystemPrompt = `你是專案長期記憶的整理者。下面是目前的長期記憶記錄，已編號。
（可能只是全部的一部分——沒列出來的不代表不存在，不要據此推論「某件事沒有被記過」。）
你的工作是找出【互相矛盾】或【已經過時】的記錄，提出修正。

判準（從嚴，寧可不動也不要亂動）：
- 只在【確實矛盾】或【確實過時】時才動。相似不等於重複——兩條講不同面向就都留著。
- 優先 UPDATE 而非 DELETE+ADD：UPDATE 保住該筆記錄的使用歷史與來源標註。
- 事實保持原子，一條講一件事。
- 標有 [user] 標籤的是【使用者本人明確要求記住的】，可以 UPDATE，但【絕對不可 DELETE】。
- 每個 UPDATE / DELETE 都必須說明理由（why）——那是人工審核的唯一依據。
- 沒有要動的就回空陣列。不要為了交差而硬找。

輸出規則：只輸出一個 JSON 物件，不要任何其他文字或 markdown 圍欄。
{"actions": [
  {"op": "update", "n": <編號>, "fact": "<改後的事實>", "why": "<為什麼要改>"},
  {"op": "delete", "n": <編號>, "why": "<為什麼要刪>"},
  {"op": "add", "fact": "<新事實>"}
]}
沒有任何要動的就輸出 {"actions": []}。`

type reconcileAction struct {
	Op   string `json:"op"`
	N    int    `json:"n"`
	Fact string `json:"fact"`
	Why  string `json:"why"`
}

// Reconcile 讀目前全部記憶記錄，請模型找出矛盾/過時的，把修正寫成【提案】。
// 產物一律只進提案通道，須 `apply memory` 放行才生效——破壞性操作尤其不能自動套用。
// 回傳實際寫入提案的條目描述（供回報）。無記錄或無事可做時回 nil, nil。
func (m *MemorySynthesizer) Reconcile(ctx context.Context) ([]string, error) {
	loader := ctxpkg.NewMemoryLoader(m.root)
	recs := loader.List()
	if len(recs) < 2 {
		return nil, nil // 少於兩筆無從矛盾
	}
	// 增量：上次整併之後沒有任何記錄變動過，就不必再花一次 LLM 呼叫。
	// 用 usedAt（帳本優先、缺則 mtime）而非固定掃全部——這也讓「連跑兩次」是 no-op。
	if last := loader.ReconciledAt(); !last.IsZero() && !anyRecordNewerThan(recs, last) {
		return nil, nil
	}
	recs, dropped := pickForReconcile(recs, maxReconcileRecords)
	if dropped > 0 {
		log.Printf("記憶整併：畫像優先送審，另有 %d 條這次沒看到（窗口 %d）", dropped, maxReconcileRecords)
	}

	var listing strings.Builder
	for i, r := range recs {
		tag := ""
		if len(r.Tags) > 0 {
			tag = " [" + strings.Join(r.Tags, ", ") + "]"
		}
		fmt.Fprintf(&listing, "%d.%s %s\n", i+1, tag, oneLine(r.Description))
	}

	resp, err := m.provider.Generate(ctx, []schema.Message{
		{Role: schema.RoleSystem, Content: reconcileSystemPrompt},
		{Role: schema.RoleUser, Content: "目前的長期記憶記錄：\n" + listing.String()},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("記憶整併 LLM 呼叫失敗: %w", err)
	}
	var out struct {
		Actions []reconcileAction `json:"actions"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil {
		// 與既有 evolve 管線一致：解析失敗就整批不動，不猜。
		return nil, fmt.Errorf("記憶整併輸出非合法 JSON（%q）: %w", resp.Content, err)
	}

	// 標記在【模型有回應之後】、寫提案之前——即使這輪判定不用動，也算整併過了，
	// 下次沒有新記錄就不必再花一次呼叫。
	loader.MarkReconciled(time.Now())

	entries := m.buildReconcileEntries(out.Actions, recs)
	if len(entries) == 0 {
		// 「模型沒提任何動作」與「提了但全被護欄擋掉」是兩件事，回報時不該長得一樣。
		log.Printf("記憶整併：模型提了 %d 個動作，通過護欄 0 個", len(out.Actions))
		return nil, nil
	}
	if err := m.appendReconciled(entries); err != nil {
		return nil, err
	}
	desc := make([]string, 0, len(entries))
	for _, e := range entries {
		desc = append(desc, describeEntry(e))
	}
	return desc, nil
}

// buildReconcileEntries 把模型動作轉成提案條目，順手把不該過的擋掉。
// 這裡是【提案時】的護欄；放行時還會再擋一次（見 ApplyProposedMemory）——
// 兩層都要，因為提案檔是人可以手改的。
func (m *MemorySynthesizer) buildReconcileEntries(actions []reconcileAction, recs []ctxpkg.MemoryRecord) []ProposedMemoryEntry {
	var entries []ProposedMemoryEntry
	for _, a := range actions {
		e := ProposedMemoryEntry{Op: strings.ToLower(strings.TrimSpace(a.Op)), Why: oneLine(a.Why)}

		if e.Op == OpAdd {
			e.Learning = oneLine(a.Fact)
			if e.Learning == "" || len(scanDangerous(e.Learning)) > 0 {
				continue
			}
			entries = append(entries, e)
			continue
		}
		if e.Op != OpUpdate && e.Op != OpDelete {
			log.Printf("記憶整併：忽略未知動作 %q", a.Op)
			continue
		}
		if a.N < 1 || a.N > len(recs) {
			log.Printf("記憶整併：編號 %d 越界（共 %d 條），視為幻覺丟棄", a.N, len(recs))
			continue
		}
		r := recs[a.N-1]

		// 護欄①：使用者本人要求記的絕不刪。UPDATE 放行（偏好會變），但要人看 diff 點頭。
		//
		// 擋下來要出聲。先前是靜默 continue，於是「模型抓到一堆矛盾但提議的全是刪畫像」會
		// 一路變成回報「沒有發現矛盾」——那不是沒找到，是找到了卻被吃掉，兩者對使用者的
		// 意義完全相反。
		if e.Op == OpDelete && hasUserTag(r.Tags) {
			log.Printf("記憶整併：擋下 DELETE %s（畫像不可刪）——理由「%s」", filepath.Base(r.Path), e.Why)
			continue
		}
		if e.Why == "" {
			log.Printf("記憶整併：忽略 %s %s（沒給理由，無從審核）", e.Op, filepath.Base(r.Path))
			continue
		}
		e.Target = strings.TrimSuffix(filepath.Base(r.Path), ".md")
		e.Old = oneLine(r.Description)
		if e.Op == OpUpdate {
			e.Learning = oneLine(a.Fact)
			if e.Learning == "" || len(scanDangerous(e.Learning)) > 0 {
				continue
			}
			if normalize(e.Learning) == normalize(e.Old) {
				continue // 改了等於沒改
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// pickForReconcile 決定哪些記錄送進 LLM，並回報有幾條沒送。
//
// 【為何不是直接切前 N 筆】呼叫端給的是 List() 的結果，依 Path 排序——而 Path 是
// `mem-<內容雜湊>.md`。切前 60 筆等於在 135 條裡隨機抽 44%，抽到誰全看雜湊。實測踩過：
// 「不開會不上板」三條裡只有一條落進窗口，對上十一條「要開會上板」，模型判成沒有矛盾——
// 它看到的資料裡確實沒有。而 MarkReconciled 一蓋章，沒被看到的那 75 條就【再也不會】被看到。
//
// 改成：畫像（tags:[user]）優先，其餘依寫入時間新到舊。理由是矛盾的代價不均勻——畫像是唯一
// 每輪全文常駐的一層，那裡的一條矛盾每回合都在生效；一般記憶要被 recall 命中才影響這一次，
// 而且模型看得到正文可以自行判斷。
func pickForReconcile(recs []ctxpkg.MemoryRecord, limit int) (picked []ctxpkg.MemoryRecord, dropped int) {
	if len(recs) <= limit {
		return recs, 0
	}
	var profile, rest []ctxpkg.MemoryRecord
	for _, r := range recs {
		if hasUserTag(r.Tags) {
			profile = append(profile, r)
		} else {
			rest = append(rest, r)
		}
	}
	// 兩組各自新到舊，同秒的用 Path 決勝——同一次呼叫裡編號要對得回記錄，排序不確定的話
	// 模型指的 3 號跟我們解讀的 3 號會不是同一筆。
	byRecent := func(s []ctxpkg.MemoryRecord) {
		sort.SliceStable(s, func(i, j int) bool {
			if !s[i].Recorded.Equal(s[j].Recorded) {
				return s[i].Recorded.After(s[j].Recorded)
			}
			return s[i].Path < s[j].Path
		})
	}
	byRecent(profile)
	byRecent(rest)

	picked = append(picked, profile...)
	if len(picked) > limit {
		picked = picked[:limit]
	}
	for _, r := range rest {
		if len(picked) >= limit {
			break
		}
		picked = append(picked, r)
	}
	return picked, len(recs) - len(picked)
}

// anyRecordNewerThan 判斷上次整併後有沒有記錄新增或被改過。
//
// 刻意用檔案 mtime 而非使用帳本的 usedAt：帳本記的是「最近被 recall」，不是「最近被改」，
// 這裡要的是後者。帳本之所以不信 mtime（見 seedMissing）是因為備份/rsync 會把 mtime 批次
// 推成 now，而那會讓【淘汰決策】反掉——代價很高。這裡誤判的代價只是多跑一次 LLM 呼叫，
// 而且方向是安全的（寧可多整併一次，不要漏掉矛盾）。
func anyRecordNewerThan(recs []ctxpkg.MemoryRecord, t time.Time) bool {
	for _, r := range recs {
		if fi, err := os.Stat(r.Path); err == nil && fi.ModTime().After(t) {
			return true
		}
	}
	return false
}

func hasUserTag(tags []string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, ctxpkg.UserProfileTag) {
			return true
		}
	}
	return false
}

func describeEntry(e ProposedMemoryEntry) string {
	switch e.Op {
	case OpUpdate:
		return fmt.Sprintf("UPDATE %s → %s", e.Target, e.Learning)
	case OpDelete:
		return fmt.Sprintf("DELETE %s（%s）", e.Target, e.Why)
	default:
		return "ADD " + e.Learning
	}
}

// appendReconciled 把整併提案追加到既有的提案檔——與 Reflect 的產物共用同一份、同一套編號，
// 所以 `memory list` / `apply memory 1 3` 完全不必知道有整併這回事。
func (m *MemorySynthesizer) appendReconciled(entries []ProposedMemoryEntry) error {
	ctxpkg.LockKnowledge()
	defer ctxpkg.UnlockKnowledge()
	if err := os.MkdirAll(filepath.Dir(m.proposedPath), 0o755); err != nil {
		return fmt.Errorf("建立提案記憶目錄失敗: %w", err)
	}
	var b strings.Builder
	if readFileIgnore(m.proposedPath) == "" {
		b.WriteString(proposedFileHeader)
	}
	fmt.Fprintf(&b, "\n## [%s] %s（掃過記錄，破壞性操作需逐條審核）\n",
		ReconcileKind, time.Now().Format(time.RFC3339))
	for _, e := range entries {
		b.WriteString(renderProposedBullet(e))
	}

	f, err := os.OpenFile(m.proposedPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("開啟提案記憶檔失敗: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("寫入提案記憶失敗: %w", err)
	}
	return nil
}
