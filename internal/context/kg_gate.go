package context

import (
	"os"
	"path/filepath"
)

// 提案邊（LLM typed 關係抽取產物）暫存於此，須過 gate 才併入生效的 edges.jsonl——同自我進化的安全鐵律。
const ProposedEdgesFile = "edges.proposed.jsonl"

const (
	minEdgeConfidence = 0.5 // 低信心 LLM 邊丟棄
	maxEdgesPerNode   = 8   // 每節點出邊上限，防 hub 爆炸
)

func kgDir(root string) string          { return filepath.Join(root, ".claw", "kg") }
func edgesPath(root string) string      { return filepath.Join(kgDir(root), "edges.jsonl") }
func proposedEdgesPath(r string) string { return filepath.Join(kgDir(r), ProposedEdgesFile) }

// ReadProposedEdges 讀提案邊（供 review）。
func ReadProposedEdges(root string) []StoredEdge { return readStoredEdges(proposedEdgesPath(root)) }

// ExistingEdges 讀生效中的邊（edges.jsonl），供外部（如 LLM 抽取去重）參考。
func ExistingEdges(root string) []StoredEdge { return readStoredEdges(edgesPath(root)) }

// ApplyProposedEdges 把提案的 typed 關係邊過 gate 後併入生效的 edges.jsonl：
//   - 丟棄信心 < 門檻、或端點不是真實節點（LLM 幻覺保護）、或重複、或超出每節點出邊上限的邊
//   - 倖存者寫進 edges.jsonl（appendEdges 再去重）；清掉提案檔
//
// 回傳 (併入數, 拒絕數)。這是 KG 自我擴充的「閘」：機器抽、規則把關、可審計。
func ApplyProposedEdges(root string) (applied, rejected int, err error) {
	proposed := readStoredEdges(proposedEdgesPath(root))
	if len(proposed) == 0 {
		return 0, 0, nil
	}
	// 真實節點集合（非 dangling stub）——端點必須存在。
	real := map[string]bool{}
	for _, r := range NewMemoryLoader(root).Records() {
		real[r.Name] = true
	}
	// 既有邊：去重基準 + 每節點出邊計數。
	seen := map[string]bool{}
	outCount := map[string]int{}
	for _, e := range readStoredEdges(edgesPath(root)) {
		seen[edgeKey(e)] = true
		outCount[e.From]++
	}

	var accepted []StoredEdge
	for _, e := range proposed {
		if e.Confidence > 0 && e.Confidence < minEdgeConfidence {
			rejected++
			continue
		}
		if !real[e.From] || !real[e.To] || e.From == e.To {
			rejected++ // 端點不存在/自環 → 幻覺保護
			continue
		}
		if seen[edgeKey(e)] {
			rejected++
			continue
		}
		if outCount[e.From] >= maxEdgesPerNode {
			rejected++ // hub 封頂
			continue
		}
		seen[edgeKey(e)] = true
		outCount[e.From]++
		accepted = append(accepted, e)
	}

	if len(accepted) > 0 {
		if _, err := appendEdges(edgesPath(root), accepted); err != nil {
			return 0, 0, err
		}
	}
	_ = os.Remove(proposedEdgesPath(root))
	return len(accepted), rejected, nil
}

// DiscardProposedEdges 丟棄提案邊。had 表示原本是否有提案。
func DiscardProposedEdges(root string) (had bool, err error) {
	if len(readStoredEdges(proposedEdgesPath(root))) == 0 {
		return false, nil
	}
	return true, os.Remove(proposedEdgesPath(root))
}

func edgeKey(e StoredEdge) string { return e.From + "\x00" + e.Type + "\x00" + e.To }
