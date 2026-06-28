package context

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Embedder 把查詢文字轉成向量。實作在 provider（OpenAI 相容 /embeddings）；未配置時呼叫端傳 nil →
// RecallGraph 退回關鍵字種子。介面放這裡（依賴反轉），避免 context→provider 的 import 迴圈。
type Embedder interface {
	EmbedQuery(text string) ([]float32, error)
}

type nodeVec struct {
	ID  string    `json:"id"`
	Vec []float32 `json:"vec"`
}

// EmbedCachePath 是節點向量快取檔（cmd/ingest -embed 產生；recall 語意選種子時讀）。
func EmbedCachePath(root string) string {
	return filepath.Join(root, ".claw", "kg", "embeddings.jsonl")
}

func readVectors(path string) map[string][]float32 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string][]float32{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var nv nodeVec
		if json.Unmarshal([]byte(line), &nv) == nil && nv.ID != "" && len(nv.Vec) > 0 {
			out[nv.ID] = nv.Vec
		}
	}
	return out
}

// WriteVectors 把節點向量寫成 embeddings.jsonl（穩定排序，便於 diff/審計）。
func WriteVectors(path string, vecs map[string][]float32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	ids := make([]string, 0, len(vecs))
	for id := range vecs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b, _ := json.Marshal(nodeVec{ID: id, Vec: vecs[id]})
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// SeedsEmbed 用查詢向量對節點向量做 cosine，取 top-n 種子（只考慮圖中真實、且有向量的節點）。
// ponytail: 暴力 cosine（節點數千內綽綽有餘）；真巨量再上 ANN 索引。
func (g *Graph) SeedsEmbed(qvec []float32, cache map[string][]float32, n int) []string {
	if len(qvec) == 0 || len(cache) == 0 {
		return nil
	}
	type sc struct {
		name string
		sim  float64
	}
	var ranked []sc
	for name, r := range g.nodes {
		if isDangling(r) {
			continue
		}
		if v, ok := cache[name]; ok {
			if s := cosine(qvec, v); s > 0 {
				ranked = append(ranked, sc{name, s})
			}
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].sim != ranked[j].sim {
			return ranked[i].sim > ranked[j].sim
		}
		return ranked[i].name < ranked[j].name
	})
	if n > 0 && len(ranked) > n {
		ranked = ranked[:n]
	}
	out := make([]string, len(ranked))
	for i, s := range ranked {
		out[i] = s.name
	}
	return out
}
