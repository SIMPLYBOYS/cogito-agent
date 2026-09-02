package tools

// verify_citations：驗證文件裡的 file:line 引用真的指到它宣稱的東西。
//
// 【為何需要這顆工具】實測 DeepWiki 對一個真實 repo 生成的架構文件（見 unity_demo 的
// docs/devin-deepwiki-對照筆記.md）：章節切法與概念對映表都是資深工程師手筆，但 file:line
// 引用是【模型猜的，不是解析的】——宣稱 openScene 在 438-460，實際在 498（438 行是一張
// SVG）。引用長得可信、驗不過，正是「假的成功比空白更糟」。
//
// 雲端服務猜行號情有可原（它沒有 repo）。我們的員工【就在 repo 旁邊】，沒有理由猜。
// 所以引用格式自帶校驗碼：路徑:行號 ＋ 那一行該有的錨點文字，
//
//	〔`internal/engine/loop.go:101` · `func (e *AgentEngine) Run`〕
//
// 這顆工具掃過整份文件、逐條打開檔案比對，錯的直接回報【實際在第幾行】——所以它不只是
// 法官，還是修正的助手：一輪就能把整頁改對。
//
// 驗不過的處置寫在 repo-wiki 技能裡：改對，或降級成只給檔名（不寫行號）。寧可少講，
// 不可講錯——一個驗不過的引用會讓整份文件的可信度歸零。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// citeRe 認 〔路徑:行號 · 錨點〕與 〔路徑:起-迄 · 錨點〕，各段的反引號可有可無。
var citeRe = regexp.MustCompile("〔\\s*`?([^`\\s:〕]+):(\\d+)(?:-(\\d+))?`?\\s*·\\s*`?([^`〕]+?)`?\\s*〕")

const (
	citeWindow     = 2  // 單行引用允許的上下容差：引用常指向區塊開頭，差一兩行不算錯
	citeMaxReport  = 20 // 壞掉的引用最多列這麼多，其餘只報數量（避免一次噴三百行）
	citeMaxAnchors = 3  // 錨點在全檔出現多次時，最多提示前幾個位置
)

type VerifyCitationsTool struct{ workDir string }

func NewVerifyCitationsTool(workDir string) *VerifyCitationsTool {
	return &VerifyCitationsTool{workDir: workDir}
}

func (t *VerifyCitationsTool) Name() string { return "verify_citations" }

func (t *VerifyCitationsTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "驗證文件裡的程式碼引用是否真的指到它宣稱的位置。引用格式：" +
			"〔路徑:行號 · 錨點文字〕或〔路徑:起-迄 · 錨點文字〕（錨點＝那幾行裡該出現的字串，" +
			"通常是函式簽名或關鍵字）。寫完任何含引用的文件都要跑一次——行號用猜的必錯，" +
			"錯的引用會讓整份文件失去可信度。回報每一條驗不過的引用【實際在第幾行】，可直接照著改。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "要檢查的文件路徑（相對工作區），如 docs/wiki/2-architecture.md"},
			},
			"required": []string{"path"},
		},
	}
}

// findAnchor 回傳錨點在 lines 中出現的所有行號（1-based）。
func findAnchor(lines []string, anchor string) []int {
	var hits []int
	for i, l := range lines {
		if strings.Contains(l, anchor) {
			hits = append(hits, i+1)
		}
	}
	return hits
}

func (t *VerifyCitationsTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("需要 path（要檢查的文件）")
	}
	docAbs, err := ResolveInWorkDir(t.workDir, in.Path)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(docAbs)
	if err != nil {
		return "", fmt.Errorf("讀不到文件: %w", err)
	}
	hits := citeRe.FindAllStringSubmatch(string(raw), -1)
	if len(hits) == 0 {
		return "這份文件裡沒有任何〔路徑:行號 · 錨點〕格式的引用。\n" +
			"如果它描述了程式碼，請補上引用——沒有引用的架構文件，讀者無從查證。", nil
	}

	cache := map[string][]string{} // 同一個檔被引用多次，只讀一次
	var bad []string
	okN := 0
	for _, m := range hits {
		path, anchor := m[1], strings.TrimSpace(m[4])
		start, _ := strconv.Atoi(m[2])
		end := start
		if m[3] != "" {
			end, _ = strconv.Atoi(m[3])
		}
		lines, seen := cache[path]
		if !seen {
			abs, errPath := ResolveInWorkDir(t.workDir, path)
			if errPath == nil {
				if b, errRead := os.ReadFile(abs); errRead == nil {
					lines = strings.Split(string(b), "\n")
				}
			}
			cache[path] = lines // 讀不到就存 nil，下次不再試
		}
		if len(lines) == 0 {
			bad = append(bad, fmt.Sprintf("✗ 〔%s:%d · %s〕\n   → 這個檔案不存在（或不在工作區內）", path, start, anchor))
			continue
		}
		// 命中窗：單行引用給 ±citeWindow 的容差（引用常指向區塊開頭）；區間引用就用區間本身
		lo, hi := start-citeWindow, end+citeWindow
		if lo < 1 {
			lo = 1
		}
		if hi > len(lines) {
			hi = len(lines)
		}
		found := false
		for i := lo; i <= hi; i++ {
			if strings.Contains(lines[i-1], anchor) {
				found = true
				break
			}
		}
		if found {
			okN++
			continue
		}
		where := findAnchor(lines, anchor)
		switch {
		case len(where) == 0:
			bad = append(bad, fmt.Sprintf("✗ 〔%s:%d · %s〕\n   → 全檔找不到這個錨點：符號不存在、拼錯，或它根本不在這個檔案裡", path, start, anchor))
		default:
			nums := make([]string, 0, citeMaxAnchors)
			for i, n := range where {
				if i >= citeMaxAnchors {
					nums = append(nums, "…")
					break
				}
				nums = append(nums, strconv.Itoa(n))
			}
			bad = append(bad, fmt.Sprintf("✗ 〔%s:%d · %s〕\n   → 行號錯了，實際在第 %s 行（照這個改）", path, start, anchor, strings.Join(nums, "、")))
		}
	}

	if len(bad) == 0 {
		return fmt.Sprintf("✓ %s：%d 條引用全部驗過，行號與錨點都對得上。", in.Path, okN), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s：%d 條引用，%d 條有問題（%d 條通過）\n\n", in.Path, len(hits), len(bad), okN)
	for i, line := range bad {
		if i >= citeMaxReport {
			fmt.Fprintf(&b, "…另有 %d 條同樣驗不過，先修完上面這些再跑一次。\n", len(bad)-citeMaxReport)
			break
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n【處置】能查到實際行號的就改對；查不到錨點的，把該說法刪掉或降級成只給檔名" +
		"（不寫行號）——寧可少講，不可講錯。改完再跑一次這顆工具。")
	return b.String(), nil
}
