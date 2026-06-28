package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

// MemEvalCase 是一筆檢索標註：query 應命中哪些記憶節點 id（人工標的 ground truth）。
type MemEvalCase struct {
	Query    string   `json:"query"`
	Expected []string `json:"expected"`
}

// LoadMemLabels 讀 JSONL 標註集（每行一個 {query, expected}）。
func LoadMemLabels(path string) ([]MemEvalCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []MemEvalCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c MemEvalCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("解析標註失敗: %w", err)
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// ModeScore 是一個檢索模式在整個標註集上的聚合指標。
type ModeScore struct {
	Mode   string  `json:"mode"`
	N      int     `json:"n"`        // 實際評到的查詢數（embedding 模式無快取時為 0）
	HitAtK float64 `json:"hit_at_k"` // 命中率：top-k 內有 ≥1 個 expected
	MRR    float64 `json:"mrr"`      // 第一個 expected 的倒數排名平均
}

// MemEvalReport 是記憶檢索 Level 1 評測報告（三模式對照）。
type MemEvalReport struct {
	K     int         `json:"k"`
	Hops  int         `json:"hops"`
	Cases int         `json:"cases"`
	Modes []ModeScore `json:"modes"`
}

// RunMemEval 對標註集跑三種檢索模式並算 hit@k / MRR：
//   - keyword：關鍵字選 top-k 種子
//   - embedding：向量語意選 top-k 種子（需 emb + 向量快取，否則略過該模式）
//   - keyword+kg：關鍵字種子 + k 跳子圖擴張（驗證 KG 多跳是否撈到種子漏掉的）
func RunMemEval(loader *ctxpkg.MemoryLoader, cases []MemEvalCase, emb ctxpkg.Embedder, k, hops int) *MemEvalReport {
	g := loader.Graph()
	var cache map[string][]float32
	if emb != nil {
		cache = loader.Vectors()
	}

	type acc struct {
		hit, rr float64
		n       int
	}
	stat := map[string]*acc{"keyword": {}, "embedding": {}, "keyword+kg": {}}
	score := func(mode string, ranked []string, exp map[string]bool) {
		a := stat[mode]
		a.n++
		if len(ranked) > k {
			ranked = ranked[:k]
		}
		for i, name := range ranked {
			if exp[name] {
				a.hit++
				a.rr += 1.0 / float64(i+1)
				break
			}
		}
	}

	for _, c := range cases {
		exp := make(map[string]bool, len(c.Expected))
		for _, e := range c.Expected {
			exp[e] = true
		}
		score("keyword", g.Seeds(c.Query, k), exp)
		if emb != nil && len(cache) > 0 {
			if qv, err := emb.EmbedQuery(c.Query); err == nil {
				score("embedding", g.SeedsEmbed(qv, cache, k), exp)
			}
		}
		nodes, _ := g.Subgraph(g.Seeds(c.Query, recallSeedsForEval), hops, k)
		score("keyword+kg", recordNames(nodes), exp)
	}

	rep := &MemEvalReport{K: k, Hops: hops, Cases: len(cases)}
	for _, mode := range []string{"keyword", "embedding", "keyword+kg"} {
		a := stat[mode]
		ms := ModeScore{Mode: mode, N: a.n}
		if a.n > 0 {
			ms.HitAtK = a.hit / float64(a.n)
			ms.MRR = a.rr / float64(a.n)
		}
		rep.Modes = append(rep.Modes, ms)
	}
	return rep
}

// recallSeedsForEval：KG 模式用較少種子，留空間給多跳鄰居浮現（對齊實際 recall 的 種子數<budget 設計）。
const recallSeedsForEval = 3

func recordNames(recs []ctxpkg.MemoryRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Name
	}
	return out
}

// Render 把報告印成對照表。
func (r *MemEvalReport) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "記憶檢索 Level 1 評測  | 查詢數 %d | k=%d hops=%d\n", r.Cases, r.K, r.Hops)
	fmt.Fprintf(&b, "%-14s %6s %8s %8s\n", "模式", "N", "hit@k", "MRR")
	for _, m := range r.Modes {
		na := fmt.Sprintf("%.2f", m.HitAtK)
		mrr := fmt.Sprintf("%.2f", m.MRR)
		if m.N == 0 {
			na, mrr = "n/a", "n/a" // 例如 embedding 未配置
		}
		fmt.Fprintf(&b, "%-14s %6d %8s %8s\n", m.Mode, m.N, na, mrr)
	}
	return b.String()
}
