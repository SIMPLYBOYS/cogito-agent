package evolve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// maxExtractNodes 限制一次餵給 LLM 的節點數，避免 prompt 爆掉（巨量需分批，屬後續優化）。
const maxExtractNodes = 40

const relationExtractSystemPrompt = `你是知識圖譜的關係抽取器。下面是一組「記憶節點」（每個有 id 與摘要/內容）。
請抽出節點【之間】有意義的有向 typed 關係，型別用簡短英文連字符詞，如：
depends-on / part-of / contradicts / relates-to / uses / example-of / supersedes。

嚴格規則：
- from 與 to 都【必須】是下面列出的節點 id（逐字照抄），不得發明新節點。
- 只抽真正有根據的關係；沒有就回空陣列。寧缺勿濫。
- 每條附 confidence（0~1）。

只輸出一個 JSON 物件，不要任何其他文字或 markdown 圍欄：
{"edges":[{"from":"<id>","to":"<id>","type":"<rel>","confidence":0.0}]}`

// RelationExtractor 用 LLM 從現有記憶節點抽出 typed 關係——這是 KG 勝 RAG 的來源（文字裡的隱含關係）。
// 產物只進【提案邊】暫存檔，須過 context.ApplyProposedEdges 才生效（同自我進化的安全鐵律）。
type RelationExtractor struct {
	provider provider.LLMProvider
	root     string
}

func NewRelationExtractor(p provider.LLMProvider, root string) *RelationExtractor {
	return &RelationExtractor{provider: p, root: root}
}

// Extract 抽關係並寫進提案邊暫存檔。端點限定在提供的節點 id 內（幻覺保護第一道；gate 還會再驗一次）。
// 回傳寫入的提案邊數。
func (e *RelationExtractor) Extract(ctx context.Context) (int, error) {
	recs := ctxpkg.NewMemoryLoader(e.root).Records()
	if len(recs) < 2 {
		return 0, nil // 不足兩節點無關係可抽
	}

	// norm：容錯 id 對應——LLM 常不逐字回傳 id（可能加 .md、改大小寫、只給 basename），
	// 用正規化表把它對回正規 id；對不上的才視為幻覺丟棄。
	norm := map[string]string{}
	addNorm := func(key, canonical string) {
		if k := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(key), ".md")); k != "" {
			norm[k] = canonical
		}
	}
	var b strings.Builder
	for i, r := range recs {
		if i >= maxExtractNodes {
			break
		}
		addNorm(r.Name, r.Name)
		if i := strings.LastIndex(r.Name, "/"); i >= 0 {
			addNorm(r.Name[i+1:], r.Name) // basename 也對得上
		}
		body := oneLine(r.Body)
		if rn := []rune(body); len(rn) > 600 {
			body = string(rn[:600])
		}
		fmt.Fprintf(&b, "- id: %s\n  摘要: %s\n  內容: %s\n", r.Name, r.Description, body)
	}
	resolve := func(s string) (string, bool) {
		c, ok := norm[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), ".md"))]
		return c, ok
	}

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: relationExtractSystemPrompt},
		{Role: schema.RoleUser, Content: "節點：\n" + b.String()},
	}
	resp, err := e.provider.Generate(ctx, msgs, nil)
	if err != nil {
		return 0, fmt.Errorf("關係抽取 LLM 調用失敗: %w", err)
	}
	var out struct {
		Edges []ctxpkg.StoredEdge `json:"edges"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil {
		return 0, fmt.Errorf("關係抽取輸出非合法 JSON（%q）: %w", resp.Content, err)
	}

	var keep []ctxpkg.StoredEdge
	for _, ed := range out.Edges {
		from, ok1 := resolve(ed.From)
		to, ok2 := resolve(ed.To)
		if ok1 && ok2 && from != to { // 端點須能對回真實節點（容錯後仍對不上＝幻覺，丟棄）
			ed.From, ed.To, ed.Source = from, to, "llm-extract"
			keep = append(keep, ed)
		}
	}
	if len(keep) == 0 {
		return 0, nil
	}
	return writeProposedEdges(e.root, keep)
}

func writeProposedEdges(root string, edges []ctxpkg.StoredEdge) (int, error) {
	dir := filepath.Join(root, ".claw", "kg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ctxpkg.ProposedEdgesFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	for _, ed := range edges {
		bs, _ := json.Marshal(ed)
		if _, err := f.Write(append(bs, '\n')); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
