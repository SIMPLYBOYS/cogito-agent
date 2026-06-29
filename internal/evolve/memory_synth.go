package evolve

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// ProposedMemoryFileName 是「提案記憶」的暫存檔（位於 .claw/ 下）。記憶自更新只寫這裡，
// 不直接動生效中的 AGENTS.md——同樣的安全鐵律：自我進化產物須人工 review 後才併入。
const ProposedMemoryFileName = "AGENTS.proposed.md"

// MemorySynthesizer 在任務成功後反思，萃取耐久的專案慣例/雷點，去重 + 安全掃描後追加到
// 【提案記憶】暫存檔；apply 時放行為 .claw/memory/ 的可檢索記憶記錄（不自動套用）。
type MemorySynthesizer struct {
	provider     provider.LLMProvider
	agentsPath   string // 生效中的 AGENTS.md（用於去重）
	proposedPath string // 提案記憶暫存檔 <root>/.claw/AGENTS.proposed.md
}

// NewMemorySynthesizer 的 root 是 AGENTS.md 所在目錄（= composer 的 workDir / AssetsDir）。
func NewMemorySynthesizer(p provider.LLMProvider, root string) *MemorySynthesizer {
	return &MemorySynthesizer{
		provider:     p,
		agentsPath:   filepath.Join(root, "AGENTS.md"),
		proposedPath: filepath.Join(root, ".claw", ProposedMemoryFileName),
	}
}

const memoryReflectSystemPrompt = `你是專案長期記憶的維護者。看完一段【已成功完成】的任務後，萃取出值得寫進
專案指南（AGENTS.md）的「耐久、可泛化」慣例或雷點——例如：建置/測試命令、repo 慣用法、容易踩的坑、
環境前置。

判準（從嚴）：
- 只保留對【未來任意任務】都有參考價值的；本次一次性的具體事實、與這次資料強綁定的內容【不要】。
- 每條寫成一句簡潔的祈使句／陳述（不要把這次的具體檔名數值寫死）。

輸出規則：只輸出一個 JSON 物件，不要任何其他文字或 markdown 圍欄。
{"learnings": ["<一句話>", "<一句話>"]}；若無值得記的，輸出 {"learnings": []}。`

// Reflect 反思一段軌跡，把新的耐久學習追加到提案記憶暫存檔。回傳實際追加的條目（去重/安全過濾後）。
func (m *MemorySynthesizer) Reflect(ctx context.Context, taskPrompt string, history []schema.Message) ([]string, error) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: memoryReflectSystemPrompt},
		{Role: schema.RoleUser, Content: fmt.Sprintf("任務：\n%s\n\n軌跡：\n%s", taskPrompt, renderTranscript(history, 6000))},
	}

	resp, err := m.provider.Generate(ctx, msgs, nil)
	if err != nil {
		return nil, fmt.Errorf("記憶反思 LLM 調用失敗: %w", err)
	}

	var out struct {
		Learnings []string `json:"learnings"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil {
		return nil, fmt.Errorf("記憶反思輸出非合法 JSON（%q）: %w", resp.Content, err)
	}
	return m.proposeLearnings(taskPrompt, out.Learnings, "慣例")
}

const failureReflectSystemPrompt = `你是負責「失敗反思」的教練。一個 agent 在與使用者的互動中嘗試完成任務但【失敗了】
（程式崩潰／達回合上限／成本熔斷／無法完成）。看完任務、執行軌跡、失敗原因後，萃取【一條】值得寫進
專案長期記憶、未來能改善「判斷與決策」的教訓。
- 聚焦「下次面對同類任務，該怎麼判斷／做不同才不會再卡」。可泛化、不要寫死本次數值。
只輸出一個 JSON 物件：{"lesson": "<一句教訓>"}；若真的沒有可記的，輸出 {"lesson": ""}。`

// ReflectFailure 在【真實互動失敗】後反思（live Reflexion）：萃取一條教訓，經同一去重+安全管道
// 追加到提案記憶。回傳實際追加的（0 或 1 條）。教訓仍是提案，須 apply 放行為記憶記錄才生效。
func (m *MemorySynthesizer) ReflectFailure(ctx context.Context, taskPrompt string, history []schema.Message, failureMsg string) ([]string, error) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: failureReflectSystemPrompt},
		{Role: schema.RoleUser, Content: fmt.Sprintf("任務：\n%s\n\n執行軌跡：\n%s\n\n失敗原因：\n%s",
			taskPrompt, renderTranscript(history, 6000), oneLine(failureMsg))},
	}
	resp, err := m.provider.Generate(ctx, msgs, nil)
	if err != nil {
		return nil, fmt.Errorf("失敗反思 LLM 調用失敗: %w", err)
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
	return m.proposeLearnings(taskPrompt, []string{out.Lesson}, "失敗教訓")
}

// proposeLearnings 對候選學習做去重（vs AGENTS.md + 已暫存提案）+ 安全掃描，安全且不重複的追加到
// 提案記憶。kind 是提案分類（如「慣例」「失敗教訓」），寫進區塊標題。回傳實際追加的。
func (m *MemorySynthesizer) proposeLearnings(taskPrompt string, candidates []string, kind string) ([]string, error) {
	existingNorm := normalize(readFileIgnore(m.agentsPath) + "\n" + readFileIgnore(m.proposedPath))

	var added []string
	seen := map[string]bool{}
	for _, l := range candidates {
		l = oneLine(l)
		if l == "" {
			continue
		}
		key := normalize(l)
		if seen[key] || strings.Contains(existingNorm, key) {
			continue // 與現有或本批重複
		}
		if hits := scanDangerous(l); len(hits) > 0 {
			continue // 危險建議（如「都用 sudo」）不入庫
		}
		seen[key] = true
		added = append(added, l)
	}

	if len(added) == 0 {
		return nil, nil
	}
	if err := m.appendProposed(taskPrompt, added, kind); err != nil {
		return nil, err
	}
	return added, nil
}

func (m *MemorySynthesizer) appendProposed(taskPrompt string, learnings []string, kind string) error {
	ctxpkg.LockKnowledge() // 只鎖檔案寫尾段（synth 的 LLM 呼叫已在更外層、不持鎖）
	defer ctxpkg.UnlockKnowledge()
	if err := os.MkdirAll(filepath.Dir(m.proposedPath), 0o755); err != nil {
		return fmt.Errorf("建立提案記憶目錄失敗: %w", err)
	}
	var b strings.Builder
	if readFileIgnore(m.proposedPath) == "" {
		b.WriteString("<!-- ⚠️ 自動生成的『提案記憶』。需人工 review 後放行（apply memory）為可檢索的長期記憶記錄才生效（不會自動套用）。 -->\n")
	}
	fmt.Fprintf(&b, "\n## [%s] 來自任務「%s」（%s）\n", kind, oneLine(taskPrompt), time.Now().Format(time.RFC3339))
	for _, l := range learnings {
		b.WriteString("- " + l + "\n")
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
func ApplyProposedMemory(root string) (string, error) {
	ctxpkg.LockKnowledge() // 整個 read-proposed→寫記錄→刪 proposed→Prune 視為一個原子單元
	defer ctxpkg.UnlockKnowledge()
	proposedPath := filepath.Join(root, ".claw", ProposedMemoryFileName)
	proposed := strings.TrimSpace(stripComments(readFileIgnore(proposedPath)))
	if proposed == "" {
		return "", nil
	}
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		return "", fmt.Errorf("建立記憶目錄失敗: %w", err)
	}
	kind, task := "記憶", ""
	for _, line := range strings.Split(proposed, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "## "):
			kind, task = parseProposedHeader(line)
		case strings.HasPrefix(line, "- "):
			learning := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if learning == "" {
				continue
			}
			if err := writeMemoryRecord(memDir, kind, task, learning); err != nil {
				return "", err
			}
		}
	}
	if err := os.Remove(proposedPath); err != nil {
		return proposed, fmt.Errorf("清除提案檔失敗: %w", err)
	}
	// 放行後順手淘汰：超過上限的最久未用記錄歸檔（可復原），避免記憶庫無限長。
	ctxpkg.NewMemoryLoader(root).Prune(maxMemoryRecords)
	return proposed, nil
}

// maxMemoryRecords 是長期記憶庫的記錄上限；超量時 Prune 把最久未用的歸檔到 .claw/memory-archive/。
const maxMemoryRecords = 200

// writeMemoryRecord 把一條學習落成可檢索記錄。slug 用內容雜湊→同一條學習冪等（重複放行覆蓋同檔，不增量）。
func writeMemoryRecord(memDir, kind, task, learning string) error {
	learning = oneLine(learning)
	h := fnv.New32a()
	_, _ = h.Write([]byte(learning))
	slug := fmt.Sprintf("mem-%08x", h.Sum32())

	title := learning // name 取短標題，過長截前 24 字（rune 安全）
	if r := []rune(learning); len(r) > 24 {
		title = string(r[:24])
	}
	body := fmt.Sprintf("---\nname: %s\ndescription: %s\ntags: [%s]\n---\n%s\n\n（來源：任務「%s」）\n",
		title, learning, kind, learning, oneLine(task))
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
func DiscardProposedMemory(root string) (had bool, err error) {
	ctxpkg.LockKnowledge()
	defer ctxpkg.UnlockKnowledge()
	proposedPath := filepath.Join(root, ".claw", ProposedMemoryFileName)
	if strings.TrimSpace(readFileIgnore(proposedPath)) == "" {
		return false, nil
	}
	return true, os.Remove(proposedPath)
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
