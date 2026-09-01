package evolve

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// ProposedMemoryFileName 是「提案記憶」的暫存檔（位於 .claw/ 下）。記憶自更新只寫這裡，
// 不直接動生效中的 AGENTS.md——同樣的安全鐵律：自我進化產物須人工 review 後才併入。
const ProposedMemoryFileName = "AGENTS.proposed.md"

// proposedFileHeader 是提案檔開頭的警語。抽成常數是因為它原本在 appendProposed 與
// writeProposedRest 各寫一份【字面值】——逐條放行會重寫整份檔案，兩邊哪天不一致就會在
// 放行後悄悄換掉警語。Reconcile 也共用同一份。
const proposedFileHeader = "<!-- ⚠️ 自動生成的『提案記憶』。需人工 review 後放行（apply memory）為可檢索的長期記憶記錄才生效（不會自動套用）。 -->\n"

// MemorySynthesizer 在任務成功後反思，萃取耐久的專案慣例/雷點，去重 + 安全掃描後追加到
// 【提案記憶】暫存檔；apply 時放行為 .claw/memory/ 的可檢索記憶記錄（不自動套用）。
type MemorySynthesizer struct {
	provider     provider.LLMProvider
	root         string // 記憶根目錄（Reconcile 要讀 .claw/memory/ 全集）
	agentsPath   string // 生效中的 AGENTS.md（用於去重）
	proposedPath string // 提案記憶暫存檔 <root>/.claw/AGENTS.proposed.md
}

// NewMemorySynthesizer 的 root 是 AGENTS.md 所在目錄（= composer 的 workDir / AssetsDir）。
func NewMemorySynthesizer(p provider.LLMProvider, root string) *MemorySynthesizer {
	return &MemorySynthesizer{
		provider:     p,
		root:         root,
		agentsPath:   filepath.Join(root, "AGENTS.md"),
		proposedPath: filepath.Join(root, ".claw", ProposedMemoryFileName),
	}
}

const memoryReflectSystemPrompt = `你是專案長期記憶的維護者。看完一段【已成功完成】的任務後，萃取出值得寫進
專案指南（AGENTS.md）的「耐久、可泛化」慣例或雷點——例如：建置/測試命令、repo 慣用法、容易踩的坑、
環境前置。

同時單獨萃取【關於使用者本人】的長期事實與偏好——他要什麼、不要什麼、慣用什麼語言/風格、
在意什麼。最高價值的訊號是「使用者當場糾正或改變你的做法」：那是偏好，不是專案慣例。

判準（從嚴）：
- 只保留對【未來任意任務】都有參考價值的；本次一次性的具體事實、與這次資料強綁定的內容【不要】。
- 每條寫成一句簡潔的祈使句／陳述（不要把這次的具體檔名數值寫死）。
- 兩者不重複：關於【專案】的放 learnings，關於【這個人】的放 user_facts。
- 【repo 自己就寫著的東西不要記】：程式結構、README/設定檔查得到的、git 歷史看得出來的——
  那不是記憶，是重複，而且會過時。要記的是「讀完程式碼也看不出來」的那部分：為什麼這樣選、
  哪裡踩過坑、什麼做法被否決過。
- 【寧缺勿濫】：沒有真正耐久的東西就兩個都給空陣列。少一條沒有損失，多一條錯的會誤導未來每一次任務。
- 【已經記過的不要再記一次】：user 訊息會附上目前記憶庫的全部內容。同一件事換個說法寫第二遍
  不會讓它更真，只會讓人多審一條、讓索引多佔一行。判準是【意思】不是字面——「派三人並行評審」
  與「用三角視角收斂決策」是同一條。要補充既有那條缺的細節時，也不要新開一條。

輸出規則：只輸出一個 JSON 物件，不要任何其他文字或 markdown 圍欄。
{"learnings": ["<一句話>"], "user_facts": ["<一句話>"]}；沒有的那項給空陣列。

每句話可在句尾用「｜觸發：<空格分隔的觸發詞>」附上檢索觸發——【什麼情況該想起這條】，
用的是未來提問裡會出現的詞，不是內容裡的詞。例：內容「單價乘 3.305785」的觸發是
「房價 坪數 實價登錄」——問的人只會說要查房價，不會說要乘什麼數。想不出觸發就不要硬附。`

// existingMemory 把目前記憶庫壓成一句一行，餵給反思當「已經知道的事」。
//
// 【為何需要】反思本來只看得到「任務 + 軌跡」，等於每一輪都在真空裡想事情。實測：31 條
// 待審提案裡有 27 條落在重複叢集——同一個意思用不同的詞寫了九遍（「派三人並行評審」／
// 「三角視角收斂」／「多角色非同步評審」…）。那不是模型笨，是我們沒給它翻舊帳的機會。
//
// 字面去重擋不住這個：那 27 條裡只有 6 條字面夠像。要靠模型自己讀懂「這是同一件事」，
// 就得讓它看得到既有的內容。
//
// 長度是有界的——Prune 把記憶庫壓在 maxMemoryRecords 以內，200 條 ≈ 4500 token，
// 用便宜模型跑一次任務不到一分錢。比事後一條條審便宜得多。
func existingMemory(root string) string {
	var b strings.Builder
	for _, r := range ctxpkg.NewMemoryLoader(root).List() {
		d := oneLine(r.Description)
		if d == "" {
			d = oneLine(r.Name)
		}
		if d != "" {
			b.WriteString("- " + d + "\n")
		}
	}
	return b.String()
}

// Reflect 反思一段軌跡，把新的耐久學習追加到提案記憶暫存檔。回傳實際追加的條目（去重/安全過濾後）。
func (m *MemorySynthesizer) Reflect(ctx context.Context, taskPrompt string, history []schema.Message) ([]string, error) {
	user := fmt.Sprintf("任務：\n%s\n\n軌跡：\n%s", taskPrompt, renderTranscript(history, 6000))
	if have := existingMemory(m.root); have != "" {
		user += "\n\n目前記憶庫已經有這些——同一件事不要再記一次：\n" + have
	}
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: memoryReflectSystemPrompt},
		{Role: schema.RoleUser, Content: user},
	}

	resp, err := m.provider.Generate(ctx, msgs, nil)
	if err != nil {
		return nil, fmt.Errorf("記憶反思 LLM 呼叫失敗: %w", err)
	}

	var out struct {
		Learnings []string `json:"learnings"`
		UserFacts []string `json:"user_facts"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil {
		return nil, fmt.Errorf("記憶反思輸出非合法 JSON（%q）: %w", resp.Content, err)
	}
	added, err := m.proposeLearnings(taskPrompt, out.Learnings, "慣例")
	if err != nil {
		return added, err
	}
	// 使用者偏好分流成 UserProfileTag——放行後正文【每輪常駐】，不像一般記憶要等 recall。
	// 同一次 LLM 呼叫順手產出，不多花一次錢。
	facts, err := m.proposeLearnings(taskPrompt, out.UserFacts, ctxpkg.UserProfileTag)
	all := append(added, facts...)
	if err == nil {
		_, err = AutoApplyAdditions(m.root, m.styleJudge(ctx)) // 四判準全中才就地放行（見 memory_autopass.go）
	}
	return all, err
}

// styleJudgeSystemPrompt：判準①的分級員。問的是「呈現方式 vs 決策行為」——
// 語言、格式、命名風格、排版屬前者；任何會改變 agent 面對任務時的判斷、步驟、工具選擇的
// 都屬後者。拿捏不準一律 false：自動放行的錯誤成本是「記了一條該人審的」，人審的成本只是
// 多看一眼——不對稱，往嚴的那邊倒。
const styleJudgeSystemPrompt = `你是記憶提案的分級員。對每一條提案回答：它是否【純風格/表達/格式偏好】——
只影響輸出的呈現方式（語言、格式、命名風格、排版、稱呼），完全不改變 agent 面對任務時的判斷、
步驟或工具選擇？會改變做事方式的（「先驗證再查」「部署前跑測試」「用某工具查某資料」）一律 false。
拿捏不準就 false。
只輸出一個 JSON 物件：{"style_only": [true/false, ...]}，陣列長度與提案數相同、順序一致。`

// styleJudge 包一個給 AutoApplyAdditions 的判準①評審。任何錯誤（呼叫失敗、JSON 壞掉、
// 長度不符）都回 nil＝全部不過——fail-closed，寧可留給人審。
func (m *MemorySynthesizer) styleJudge(ctx context.Context) StyleJudge {
	return func(learnings []string) []bool {
		var b strings.Builder
		for i, l := range learnings {
			fmt.Fprintf(&b, "%d. %s\n", i+1, oneLine(l))
		}
		resp, err := m.provider.Generate(ctx, []schema.Message{
			{Role: schema.RoleSystem, Content: styleJudgeSystemPrompt},
			{Role: schema.RoleUser, Content: b.String()},
		}, nil)
		if err != nil {
			return nil
		}
		var out struct {
			StyleOnly []bool `json:"style_only"`
		}
		if json.Unmarshal([]byte(extractJSON(resp.Content)), &out) != nil ||
			len(out.StyleOnly) != len(learnings) {
			return nil
		}
		return out.StyleOnly
	}
}

const failureReflectSystemPrompt = `你是負責「失敗反思」的教練。一個 agent 在與使用者的互動中嘗試完成任務但【失敗了】
（程式崩潰／達回合上限／成本熔斷／無法完成）。看完任務、執行軌跡、失敗原因後，萃取【一條】值得寫進
專案長期記憶、未來能改善「判斷與決策」的教訓。
- 聚焦「下次面對同類任務，該怎麼判斷／做不同才不會再卡」。可泛化、不要寫死本次數值。
只輸出一個 JSON 物件：{"lesson": "<一句教訓>"}；若真的沒有可記的，輸出 {"lesson": ""}。
教訓句尾可用「｜觸發：<空格分隔的觸發詞>」附上檢索觸發（未來什麼樣的任務該想起這條）。`

// ReflectFailure 在【真實互動失敗】後反思（live Reflexion）：萃取一條教訓，經同一去重+安全管道
// 追加到提案記憶。回傳實際追加的（0 或 1 條）。教訓仍是提案，須 apply 放行為記憶記錄才生效。
func (m *MemorySynthesizer) ReflectFailure(ctx context.Context, taskPrompt string, history []schema.Message, failureMsg string) ([]string, error) {
	// 失敗反思走同一條路：它也是往同一個記憶庫寫，不給它看既有的就會生出同義的第二條。
	user := fmt.Sprintf("任務：\n%s\n\n執行軌跡：\n%s\n\n失敗原因：\n%s",
		taskPrompt, renderTranscript(history, 6000), oneLine(failureMsg))
	if have := existingMemory(m.root); have != "" {
		user += "\n\n目前記憶庫已經有這些——同一件事不要再記一次：\n" + have
	}
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: failureReflectSystemPrompt},
		{Role: schema.RoleUser, Content: user},
	}
	resp, err := m.provider.Generate(ctx, msgs, nil)
	if err != nil {
		return nil, fmt.Errorf("失敗反思 LLM 呼叫失敗: %w", err)
	}
	var out struct {
		Lesson string `json:"lesson"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil {
		return nil, fmt.Errorf("失敗反思輸出非合法 JSON（%q）: %w", resp.Content, err)
	}
	if strings.TrimSpace(out.Lesson) == "" {
		return nil, nil
	}
	added, err := m.proposeLearnings(taskPrompt, []string{out.Lesson}, "失敗教訓")
	if err == nil {
		_, err = AutoApplyAdditions(m.root, m.styleJudge(ctx))
	}
	return added, err
}

// proposeLearnings 對候選學習做去重（vs AGENTS.md + 已暫存提案）+ 安全掃描，安全且不重複的追加到
// 提案記憶。kind 是提案分類（如「慣例」「失敗教訓」），寫進區塊標題。回傳實際追加的。
func (m *MemorySynthesizer) proposeLearnings(taskPrompt string, candidates []string, kind string) ([]string, error) {
	existingNorm := normalize(readFileIgnore(m.agentsPath) + "\n" + readFileIgnore(m.proposedPath))

	var added, bullets []string
	seen := map[string]bool{}
	for _, l := range candidates {
		l = oneLine(l)
		// 反思器可在句尾用「｜觸發：房價 坪數」附檢索觸發詞。先拆下來：去重與安全掃描
		// 只該看事實本身——同一條事實配不同觸發詞，仍然是同一條記憶。
		var trig string
		if fact, t, ok := strings.Cut(l, "｜觸發："); ok {
			l, trig = strings.TrimSpace(fact), strings.TrimSpace(t)
		}
		if l == "" {
			continue
		}
		key := normalize(l)
		if seen[key] || strings.Contains(existingNorm, key) {
			continue // 與現有或本批重複
		}
		if hits := scanDangerous(l + " " + trig); len(hits) > 0 {
			continue // 危險建議（如「都用 sudo」）不入庫——觸發詞也掃，別留後門
		}
		seen[key] = true
		added = append(added, l)
		bullets = append(bullets, renderProposedBullet(ProposedMemoryEntry{Learning: l, Trigger: trig}))
	}

	if len(added) == 0 {
		return nil, nil
	}
	if err := m.appendProposed(taskPrompt, bullets, kind); err != nil {
		return nil, err
	}
	return added, nil
}

func (m *MemorySynthesizer) appendProposed(taskPrompt string, bullets []string, kind string) error {
	ctxpkg.LockKnowledge() // 只鎖檔案寫尾段（synth 的 LLM 呼叫已在更外層、不持鎖）
	defer ctxpkg.UnlockKnowledge()
	if err := os.MkdirAll(filepath.Dir(m.proposedPath), 0o755); err != nil {
		return fmt.Errorf("建立提案記憶目錄失敗: %w", err)
	}
	var b strings.Builder
	if readFileIgnore(m.proposedPath) == "" {
		b.WriteString(proposedFileHeader)
	}
	fmt.Fprintf(&b, "\n## [%s] 來自任務「%s」（%s）\n", kind, oneLine(taskPrompt), time.Now().Format(time.RFC3339))
	for _, blk := range bullets {
		b.WriteString(blk) // 已含「- 」前綴與可能的續行（觸發：…）——渲染統一走 renderProposedBullet
	}

	f, err := os.OpenFile(m.proposedPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("開啟提案記憶檔失敗: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("追加提案記憶失敗: %w", err)
	}
	return nil
}

// ApplyProposedMemory 把提案記憶放行為【離散的可檢索記錄】（.claw/memory/<slug>.md），而非
// append 進 AGENTS.md——後者會讓常駐 System Prompt 無限膨脹。每條學習落成一筆記錄，由 recall 工具
// 按需檢索。人工 review 後手動觸發；放行後清掉提案檔。回傳放行的內容（空字串＝當前沒有提案）。
// ProposedMemoryEntry 是提案記憶的一條，供【逐條】審核。N 為 1-based 編號，對應 `apply memory <N>`。
// Header 保留該條所屬的 "## [kind] 來自任務「…」（…）" 原文——只放行部分條目時要靠它把剩下的
// 寫回提案檔而不丟失分組與時間。
type ProposedMemoryEntry struct {
	N        int
	Header   string
	Kind     string
	Task     string
	Learning string // add：新事實；update：改後的值；delete：空（原值在 Old）

	// 以下供【整併】提案（Reconcile 產出）表達破壞性操作。Op 空字串等同 OpAdd——
	// 舊格式的提案檔（純 bullet）解析出來就是這個狀態，向後相容不必特判。
	Op     string // "" / OpAdd / OpUpdate / OpDelete
	Target string // update/delete 的目標記錄 slug（.claw/memory/<slug>.md 去掉副檔名）
	Old    string // update 的舊值 / delete 的原值——人審時看 diff 用，放行時當樂觀鎖比對
	Why    string // 為何要動它。人審的依據，update/delete 必填
	// Trigger 是這條記憶的檢索觸發詞（「什麼情況該想起我」），與內容分離——內容說
	// 「每平方公尺乘 3.305785」，觸發是「房價 坪數」。選填；放行時寫進記錄的 frontmatter。
	Trigger string
}

// 提案動作。設計與理由見 docs/memory-reconcile-format.md。
const (
	OpAdd    = "add"
	OpUpdate = "update"
	OpDelete = "delete"
)

// IsDestructive 回報這條會不會動到既有記錄——render 要據此加警示，放行要據此走護欄。
func (e ProposedMemoryEntry) IsDestructive() bool {
	return e.Op == OpUpdate || e.Op == OpDelete
}

// ListProposedMemory 把提案檔解析成逐條清單（無提案回空）。供 `memory list` 顯示編號，
// 以及逐條放行／丟棄。
func ListProposedMemory(root string) []ProposedMemoryEntry {
	return parseProposedMemory(readFileIgnore(filepath.Join(root, ".claw", ProposedMemoryFileName)))
}

// maxConflictHits 是每條提案最多列幾條疑似相關的既有記憶。列太多等於沒標——
// 審核的成本本來就是「要看幾條」，把它從 157 降到 2 才是重點。
const maxConflictHits = 2

// ConflictHits 找出跟這條提案【最可能相關】的既有記憶。
//
// 為什麼要有：一條一條審的真正成本不在判斷，在【搜尋】——要先想起記憶庫裡有沒有相關的、
// 再去翻。157 條的時候那件事做不了，於是提案就堆著不審。呈現前先跑一次，人只做判斷。
//
// ⚠ 只標【疑似】。純文字相似度分不出「重複」與「矛盾」——那要讀懂兩句話的意思。
// 但它分得出「這條跟哪幾條有關」，而那正是省下來的部分。
//
// UPDATE/DELETE 不必猜：它們本來就指名了 Target，那條就是受影響的條目。
func ConflictHits(root string, e ProposedMemoryEntry) []ctxpkg.MemoryRecord {
	loader := ctxpkg.NewMemoryLoader(root)
	if e.Target != "" {
		for _, r := range loader.List() {
			if strings.TrimSuffix(filepath.Base(r.Path), ".md") == e.Target {
				return []ctxpkg.MemoryRecord{r}
			}
		}
		return nil
	}
	// Related 而非 Recall：掃描不是「用到」，記進帳本會污染索引排序與淘汰決策。
	return loader.Related(e.Learning, maxConflictHits)
}

// parseProposedMemory 解析提案檔。文法見 docs/memory-reconcile-format.md：
//
//	## [kind] 來自任務「…」（ts）     ← 分組標頭
//	- 純文字                          ← ADD（舊格式，原樣支援）
//	- UPDATE <slug> — <新值>          ← 改寫既有記錄
//	  舊：<原值>                      ← 縮排附帶行，屬於上一條 bullet
//	  因：<理由>
//	- DELETE <slug>
//	  值：<原值>
//	  因：<理由>
//
// 【編號規則不動】一個 bullet 一條、按掃描順序給 N——`apply memory 1 3` 因此完全不必改。
// 附帶行縮排且不以 "-" 開頭，舊解析器讀到會忽略，是安全的漸進升級。
func parseProposedMemory(raw string) []ProposedMemoryEntry {
	var out []ProposedMemoryEntry
	header, kind, task := "", "記憶", ""
	for _, raw := range strings.Split(stripComments(raw), "\n") {
		// 縮排判斷要在 TrimSpace 之前做：附帶行靠縮排歸屬上一條 bullet。
		indented := raw != strings.TrimLeft(raw, " \t")
		line := strings.TrimSpace(raw)

		switch {
		case strings.HasPrefix(line, "## "):
			header = line
			kind, task = parseProposedHeader(line)

		case strings.HasPrefix(line, "- "):
			body := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if body == "" {
				continue
			}
			e := ProposedMemoryEntry{N: len(out) + 1, Header: header, Kind: kind, Task: task}
			parseAction(&e, body)
			out = append(out, e)

		case indented && len(out) > 0:
			// 附帶行的規則分兩級：動作型（UPDATE/DELETE）吃全部欄位（舊/值/因/觸發）；
			// 純 ADD 只認「觸發：」這一種——其他縮排文字照舊忽略，免得舊檔裡碰巧的縮排
			// 被誤讀成欄位（這是原本整條 else 不存在的理由，防護保留、只開一個白名單口）。
			e := &out[len(out)-1]
			if e.IsDestructive() || strings.HasPrefix(line, "觸發：") || strings.HasPrefix(line, "觸發:") {
				attachMeta(e, line)
			}
		}
	}
	return out
}

// parseAction 認 bullet 的動作前綴。沒有前綴＝ADD（舊格式行為）。
// 刻意不在這裡拒絕殘缺的動作（例如 UPDATE 沒帶新值）——留著讓放行路徑報明確原因，
// 直接丟掉會讓編號位移、使用者看到的清單與檔案對不上。
func parseAction(e *ProposedMemoryEntry, body string) {
	for _, a := range []struct{ verb, op string }{{"UPDATE ", OpUpdate}, {"DELETE ", OpDelete}} {
		if !strings.HasPrefix(body, a.verb) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(body, a.verb))
		slug, tail, _ := strings.Cut(rest, " ")
		// 分隔符寬鬆些：我們寫「— 」，但模型可能吐 "-" 或 ":"，且可能【黏在 slug 上】
		// （"mem-x: 新值"）。slug 右側只剝 ":" 與 "—"——"-" 是 slug 內容的一部分
		// （mem-1a2b3c4d），一併剝掉會咬到合法字元。
		e.Op = a.op
		e.Target = strings.TrimRight(strings.TrimSpace(slug), "—:")
		e.Learning = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(tail), "—-:"))
		return
	}
	e.Op, e.Learning = OpAdd, body
}

// attachMeta 把縮排附帶行填進上一條動作提案。未知前綴忽略（容忍模型多寫幾行）。
func attachMeta(e *ProposedMemoryEntry, line string) {
	for _, m := range []struct {
		prefixes []string
		dst      *string
	}{
		{[]string{"舊：", "舊:", "值：", "值:"}, &e.Old},
		{[]string{"因：", "因:"}, &e.Why},
		{[]string{"觸發：", "觸發:"}, &e.Trigger},
	} {
		for _, p := range m.prefixes {
			if strings.HasPrefix(line, p) {
				*m.dst = strings.TrimSpace(strings.TrimPrefix(line, p))
				return
			}
		}
	}
}

// pickProposed 依 only（1-based 編號；空＝全選）把條目切成「選中」與「留下」。編號超出範圍即忽略。
func pickProposed(all []ProposedMemoryEntry, only []int) (picked, rest []ProposedMemoryEntry) {
	if len(only) == 0 {
		return all, nil
	}
	sel := map[int]bool{}
	for _, n := range only {
		sel[n] = true
	}
	for _, e := range all {
		if sel[e.N] {
			picked = append(picked, e)
		} else {
			rest = append(rest, e)
		}
	}
	return picked, rest
}

// renderProposedBullet 把一條提案寫回檔案格式。**必須與 parseProposedMemory 對稱**——
// 逐條放行時未選中的條目要原樣留在檔裡；少了這層，一條 UPDATE 提案只要被跳過一次就會
// 退化成純文字 ADD，下次放行等於憑空多一筆記憶。
func renderProposedBullet(e ProposedMemoryEntry) string {
	var b strings.Builder
	switch e.Op {
	case OpUpdate:
		fmt.Fprintf(&b, "- UPDATE %s — %s\n", e.Target, e.Learning)
	case OpDelete:
		fmt.Fprintf(&b, "- DELETE %s\n", e.Target)
	default:
		fmt.Fprintf(&b, "- %s\n", e.Learning)
		if e.Trigger != "" {
			fmt.Fprintf(&b, "  觸發：%s\n", e.Trigger)
		}
		return b.String()
	}
	if e.Trigger != "" {
		fmt.Fprintf(&b, "  觸發：%s\n", e.Trigger)
	}
	if e.Old != "" {
		label := "舊"
		if e.Op == OpDelete {
			label = "值"
		}
		fmt.Fprintf(&b, "  %s：%s\n", label, e.Old)
	}
	if e.Why != "" {
		fmt.Fprintf(&b, "  因：%s\n", e.Why)
	}
	return b.String()
}

// writeProposedRest 把「留下」的條目寫回提案檔（保留原分組標頭）；沒有剩餘就刪檔。
func writeProposedRest(path string, rest []ProposedMemoryEntry) error {
	if len(rest) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清除提案檔失敗: %w", err)
		}
		return nil
	}
	var b strings.Builder
	b.WriteString(proposedFileHeader)
	last := ""
	for _, e := range rest {
		if e.Header != last {
			fmt.Fprintf(&b, "\n%s\n", e.Header)
			last = e.Header
		}
		b.WriteString(renderProposedBullet(e))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// ApplyProposedMemory 把提案記憶放行為【離散的可檢索記錄】（.claw/memory/<slug>.md），而非
// append 進 AGENTS.md——後者會讓常駐 System Prompt 無限膨脹。人工 review 後手動觸發。
//
// only 為 1-based 編號（見 ListProposedMemory）；**留空＝全部放行**（保留原本的批次語意）。
// 逐條放行的理由：先前是全有全無，一批裡有一條寫壞就得整批丟掉——而反思是批次產出的，
// 「大致有用但夾一條爛的」才是常態。未選中的條目留在提案檔等後續處置。
// skipped 回報「選中了但沒套用」的原因。破壞性操作一定要有這個——使用者按了
// `apply memory 3` 卻沒動靜又沒解釋，比直接失敗更糟。被跳過的條目【留在提案檔】等重新整併。
func ApplyProposedMemory(root string, only ...int) (applied, skipped []string, err error) {
	ctxpkg.LockKnowledge() // 整個 read-proposed→寫記錄→回寫剩餘→Prune 視為一個原子單元
	defer ctxpkg.UnlockKnowledge()
	proposedPath := filepath.Join(root, ".claw", ProposedMemoryFileName)
	all := parseProposedMemory(readFileIgnore(proposedPath))
	if len(all) == 0 {
		return nil, nil, nil
	}
	picked, rest := pickProposed(all, only)
	if len(picked) == 0 {
		return nil, nil, nil // 編號都不存在：不動任何東西
	}
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("建立記憶目錄失敗: %w", err)
	}
	loader := ctxpkg.NewMemoryLoader(root)
	// 先把散落在記憶目錄的既有變化收成一個 commit（崩潰殘留、手動編修）——
	// 少這步，第一條放行的 commit 會把它們全掃進來，revert 單條就不再是單條。
	commitMemory(root, "收整散落變化（非本次放行）")
	for _, e := range picked {
		if !e.IsDestructive() {
			if err := writeMemoryRecord(memDir, e.Kind, e.Task, e.Learning, e.Trigger); err != nil {
				return applied, skipped, err
			}
			applied = append(applied, e.Learning)
			commitMemory(root, "放行 "+e.Learning) // 一提案一 commit：revert 即回滾單條
			continue
		}
		note, err := applyDestructive(loader, memDir, e)
		if err != nil {
			return applied, skipped, err
		}
		if note != "" { // 護欄擋下：留在提案檔，回報原因
			skipped = append(skipped, fmt.Sprintf("#%d %s：%s", e.N, e.Target, note))
			rest = append(rest, e)
			continue
		}
		applied = append(applied, describeEntry(e))
		commitMemory(root, "放行 "+describeEntry(e))
	}
	// 被跳過的塞回 rest，順序會亂掉——依原編號排回去，回寫的檔案才與使用者看過的清單一致。
	sort.Slice(rest, func(i, j int) bool { return rest[i].N < rest[j].N })
	if err := writeProposedRest(proposedPath, rest); err != nil {
		return applied, skipped, err
	}
	// 放行後順手淘汰：超過上限的最久未用記錄歸檔（可復原），避免記憶庫無限長。
	loader.Prune(maxMemoryRecords)
	commitMemory(root, "淘汰歸檔（超過記錄上限，最久未用）")
	return applied, skipped, nil
}

// EnvAutoApply=1：反思產出的【新增】提案立刻放行成記錄，不等人工 `apply memory`。
const EnvAutoApply = "COGITO_MEMORY_AUTOAPPLY"

// additionCandidates 取提案檔中可考慮自動放行的條目：非破壞性（判準②——刪改是不可逆的，
// 永遠人審）、非使用者畫像（畫像是在猜人，錯的畫像不會被下次讀碼推翻）。四條判準的其餘
// 三條在 AutoApplyAdditions（memory_autopass.go）。單獨鎖一小段而不與 ApplyProposedMemory
// 共用同一次上鎖：knowledgeMu 不可重入。提案檔是 append-only，編號不會位移，空窗安全。
func additionCandidates(root string) []ProposedMemoryEntry {
	ctxpkg.LockKnowledge()
	defer ctxpkg.UnlockKnowledge()
	var out []ProposedMemoryEntry
	for _, e := range parseProposedMemory(readFileIgnore(filepath.Join(root, ".claw", ProposedMemoryFileName))) {
		if !e.IsDestructive() && !isUserProfile(e.Kind) {
			out = append(out, e)
		}
	}
	return out
}

// PendingProposals 回傳提案檔裡還沒放行的條數。供通知措辭判斷「是不是真的全部生效了」——
// 自動放行只吃專案慣例，使用者畫像那類會留下來，一律說「已生效」就是在說謊。
func PendingProposals(root string) int {
	ctxpkg.LockKnowledge()
	defer ctxpkg.UnlockKnowledge()
	return len(parseProposedMemory(readFileIgnore(filepath.Join(root, ".claw", ProposedMemoryFileName))))
}

// isUserProfile 判斷這條提案是不是「關於使用者本人」的畫像。
//
// 這類【不】自動放行，即使開了 AUTOAPPLY。理由是實測掃過 123 條之後看出來的：
// 「慣例」類的品質普遍好（專案事實、踩過的坑、可複用的做法都能從軌跡驗證），
// 但「user」類是在猜『這個人是什麼樣的人』——樣本卻只有幾句一次性的指令。
// 於是「我為了省錢說的那句『不開會不上板』」被記成他的偏好，跟另外六條「開工前
// 一定要看板子」直接打架；成本熔斷逼出來的行為也被當成他的習慣。
//
// 錯的專案事實下次讀程式碼就會被推翻；錯的【人物畫像】不會——它會一直影響
// agent 怎麼跟人互動，而且沒有任何客觀證據能推翻它。所以這類一律留給人過目。
func isUserProfile(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), ctxpkg.UserProfileTag)
}

// applyDestructive 套用 UPDATE / DELETE。回傳非空字串＝被護欄擋下的原因（呼叫端據此把
// 該條留回提案檔）；回傳空字串＝已套用。
//
// 這是【放行時】的第二層護欄。提案時已經擋過一輪，但提案檔是純文字、人可以手改，
// 所以真正動檔案的這一步必須自己再驗一次，不能信提案內容。
func applyDestructive(loader *ctxpkg.MemoryLoader, memDir string, e ProposedMemoryEntry) (string, error) {
	if e.Target == "" {
		return "提案殘缺（缺目標記錄）", nil
	}
	if e.Op == OpUpdate && e.Learning == "" {
		return "提案殘缺（缺新值）", nil
	}

	var rec *ctxpkg.MemoryRecord
	for _, r := range loader.List() {
		if strings.TrimSuffix(filepath.Base(r.Path), ".md") == e.Target {
			rec = &r
			break
		}
	}
	if rec == nil {
		return "記錄已不存在（可能已被歸檔或放行過）", nil
	}

	// 護欄①：使用者本人明確要求記住的，不可刪。
	if e.Op == OpDelete && hasUserTag(rec.Tags) {
		return "使用者畫像記錄不可刪除", nil
	}
	// 護欄②：樂觀鎖。提案產生到放行之間記錄可能已被改過——舊值對不上就拒絕，
	// 否則會拿一份過期的 diff 去覆蓋現況。
	if e.Old != "" && normalize(rec.Description) != normalize(e.Old) {
		return "記錄內容已變動，請重新整併", nil
	}

	if e.Op == OpDelete {
		// 護欄③：歸檔而非刪除。
		return "", loader.ArchiveRecord(filepath.Base(rec.Path))
	}
	title := memoryTitle(e.Learning)
	note := fmt.Sprintf("〔整併 provenance〕於 %s 由整併提案改寫；原內容：%s",
		time.Now().Format(time.RFC3339), e.Old)
	return "", ctxpkg.UpdateRecordFact(rec.Path, title, e.Learning, note)
}

// maxMemoryRecords 是長期記憶庫的記錄上限；超量時 Prune 把最久未用的歸檔到 .claw/memory-archive/。
const maxMemoryRecords = 200

// memoryTitleRunes 是記錄 frontmatter `name:` 的長度上限。抽成常數是因為整併的 UPDATE
// 也要套同一規則——兩處各寫一個 24 遲早會分岔。
const memoryTitleRunes = 24

// memoryTitle 從一條學習取短標題，且【切在句讀邊界】而不是硬切第 24 個字。
//
// 【為何重要】name 同時是知識圖譜的節點 ID 與 [[link]] 的指向目標。硬切會產出
// 「外部工具（MCP/API）查不到或未掛載時，**」這種字串——沒有人（或 LLM）寫得出
// [[外部工具（MCP/API）查不到或未掛載時，**]]，於是圖永遠長不出人工邊（實測 14 個節點 0 條邊）。
//
// 做法：在上限內找最後一個句讀，切在那裡；找不到才退回硬切。切完再剝掉尾端殘留的
// markdown 記號與孤立的開引號——那些是「斷在標記中間」的痕跡，留著同樣不能當連結目標。
func memoryTitle(learning string) string {
	r := []rune(oneLine(learning))
	if len(r) <= memoryTitleRunes {
		return strings.TrimSpace(trimTitleEdge(string(r)))
	}
	head := r[:memoryTitleRunes]
	// 依【標點強度】分層找切點，不是找最後一個能切的地方。
	//
	// 實測教訓：把空格也當切點時，「實價登錄單價欄位為每平方公尺，換算每坪需乘以 3.305785」
	// 會切在最後那個空格→「…換算每坪需乘以」，比切在更前面的「，」→「實價登錄單價欄位為每平方公尺」
	// 差得多。中英混排的空格只是語言邊界不是語意邊界，所以【完全不採用】。
	for _, tier := range []string{"。！？!?", "，、；：,;:", "）」』〕】)]"} {
		if i := lastIndexAny(head, tier); i >= minTitleRunes {
			return strings.TrimSpace(trimTitleEdge(string(head[:i+1])))
		}
	}
	return strings.TrimSpace(trimTitleEdge(string(head)))
}

// minTitleRunes 是切點的下限：切到只剩幾個字等於沒有標題，那還不如硬切滿。
const minTitleRunes = 8

// lastIndexAny 回 head 裡最後一個屬於 set 的 rune 索引；沒有回 -1。
func lastIndexAny(head []rune, set string) int {
	for i := len(head) - 1; i >= 0; i-- {
		if strings.ContainsRune(set, head[i]) {
			return i
		}
	}
	return -1
}

// trimTitleEdge 清掉切點留下的殘渣。【順序有意義】：
//
//  1. 先剝尾端的記號與標點——因為 TrimRight 自己會【製造】不平衡：
//     「外部工具**起初查無不代表永久不可用**」尾端那組 ** 被剝掉之後，前面那組就落單了。
//     先剝再配對，配對看到的才是最終字串。
//  2. 再配對括號與記號。
//  3. 最後只剝尾端【標點】（不含記號），避免第 3 步又把剛配好的記號拆掉。
func trimTitleEdge(s string) string {
	const edgeAll = "*_`~#（(「『〔【[<，、；：-— "
	const edgePunct = "（(「『〔【[<，、；：-— "
	s = strings.TrimRight(s, edgeAll)
	s = stripMarkers(s)
	s = balanceBrackets(s)
	return strings.TrimRight(s, edgePunct)
}

// titleBrackets 是要配對的括號（開→閉）。引號類也算。
var titleBrackets = map[rune]rune{'（': '）', '(': ')', '「': '」', '『': '』', '〔': '〕', '【': '】', '[': ']'}

// balanceBrackets 砍到第一個未閉合的開括號之前。已全數閉合則原樣回傳。
func balanceBrackets(s string) string {
	var stack []int
	r := []rune(s)
	for i, c := range r {
		if _, ok := titleBrackets[c]; ok {
			stack = append(stack, i)
			continue
		}
		if len(stack) > 0 && c == titleBrackets[r[stack[len(stack)-1]]] {
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) == 0 {
		return s
	}
	return string(r[:stack[0]])
}

// titleMarkers 是標題裡要【整個剝掉】的 markdown 強調記號。
//
// 剝掉而不是「切在未閉合處」：強調記號在標題裡沒有任何語意，切掉反而丟內容——
// 「外部工具**起初查無不代表永久不可用**；」若切在未閉合的 ** 只剩「外部工具」，
// 剝掉則得到完整的「外部工具起初查無不代表永久不可用」。實測踩過。
//
// 刻意【不含 _】：底線在技術文字裡是識別字的一部分（query_rows、tool_name），
// 當成強調記號會把「查詢開放資料前先用 query_rows」砍成「…先用 query」。也實測踩過。
var titleMarkers = []string{"**", "`"}

// stripMarkers 把強調記號從標題中移除（不影響其餘文字）。
func stripMarkers(s string) string {
	for _, m := range titleMarkers {
		s = strings.ReplaceAll(s, m, "")
	}
	return s
}

// writeMemoryRecord 把一條學習落成可檢索記錄。slug 用內容雜湊→同一條學習冪等（重複放行覆蓋同檔，不增量）。

// memSlug 由內容算記錄檔名（不含副檔名）。內容定址是撤回窗能「事後對回檔案」的前提：
// 帳上只記內容，檔名永遠重算得出來。
func memSlug(learning string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(oneLine(learning)))
	return fmt.Sprintf("mem-%08x", h.Sum32())
}

func writeMemoryRecord(memDir, kind, task, learning, trigger string) error {
	learning = oneLine(learning)
	slug := memSlug(learning)

	title := memoryTitle(learning) // name 是節點 ID＋[[link]] 目標，切在句讀邊界
	// 來源標註（provenance，對抗幻覺記憶）：frontmatter 記時間戳、body 記完整來源；body 會在 recall
	// 時渲染給模型看，讓「檢索到的真實記憶」自帶「由誰、何時、從哪個任務沉澱」——可溯源、可稽核、
	// 和模型自產內容區分。時間戳同時作為 last-recorded（同一條學習重複放行＝重新確認）。
	ts := time.Now().Format(time.RFC3339)
	trigLine := ""
	if t := oneLine(trigger); t != "" {
		trigLine = "trigger: " + t + "\n" // 檢索觸發詞（scoreRecord 最高權重）；缺省＝行為同舊記錄
	}
	body := fmt.Sprintf("---\nname: %s\ndescription: %s\n%stags: [%s]\nrecorded: %s\n---\n%s\n\n〔來源 provenance〕由「%s」反思、於 %s 從任務「%s」沉澱。\n",
		title, learning, trigLine, kind, ts, learning, kind, ts, oneLine(task))
	return os.WriteFile(filepath.Join(memDir, slug+".md"), []byte(body), 0o644)
}

// parseProposedHeader 從提案區塊標題「## [慣例] 來自任務「X」（ts）」抽出分類與任務，作記錄的 tag/溯源。
func parseProposedHeader(line string) (kind, task string) {
	kind, task = "記憶", ""
	if i, j := strings.Index(line, "["), strings.Index(line, "]"); i >= 0 && j > i {
		kind = line[i+1 : j]
	}
	if i := strings.Index(line, "「"); i >= 0 {
		if j := strings.Index(line, "」"); j > i {
			task = line[i+len("「") : j]
		}
	}
	return kind, task
}

// DiscardProposedMemory 丟棄提案記憶。had 表示原本是否有提案。
// only 為 1-based 編號（見 ListProposedMemory）；留空＝全部丟棄。回傳實際丟棄的條目。
func DiscardProposedMemory(root string, only ...int) (dropped []string, err error) {
	ctxpkg.LockKnowledge()
	defer ctxpkg.UnlockKnowledge()
	proposedPath := filepath.Join(root, ".claw", ProposedMemoryFileName)
	all := parseProposedMemory(readFileIgnore(proposedPath))
	if len(all) == 0 {
		return nil, nil
	}
	picked, rest := pickProposed(all, only)
	if len(picked) == 0 {
		return nil, nil
	}
	for _, e := range picked {
		dropped = append(dropped, e.Learning)
	}
	return dropped, writeProposedRest(proposedPath, rest)
}

// stripComments 去掉 HTML 註解行（提案檔頂部的「需 review」提示，併入後已無意義）。
func stripComments(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "<!--") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

func readFileIgnore(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// normalize 把字串轉小寫並壓平空白，供寬鬆去重比對。
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
