package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEmbedder：把查詢映射成固定向量，讓語意選種子可離線、確定性地測。
type fakeEmbedder struct{ vec []float32 }

func (f fakeEmbedder) EmbedQuery(string) ([]float32, error) { return f.vec, nil }

func TestCosineAndWriteReadVectors(t *testing.T) {
	if c := cosine([]float32{1, 0}, []float32{1, 0}); c < 0.999 {
		t.Errorf("同向量 cosine 應≈1，got %v", c)
	}
	if c := cosine([]float32{1, 0}, []float32{0, 1}); c != 0 {
		t.Errorf("正交向量 cosine 應為 0，got %v", c)
	}
	path := filepath.Join(t.TempDir(), "embeddings.jsonl")
	in := map[string][]float32{"a": {1, 0}, "b": {0, 1}}
	if err := WriteVectors(path, in); err != nil {
		t.Fatal(err)
	}
	got := readVectors(path)
	if len(got) != 2 || got["a"][0] != 1 || got["b"][1] != 1 {
		t.Errorf("向量讀寫往返錯誤: %+v", got)
	}
}

// embedding 選種子應挑語意最近者；且設了 emb + 快取時 RecallGraph 走語意路徑。
func TestRecallGraph_EmbeddingSeeds(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	// 兩節點，關鍵字都不含查詢詞 "zzz"——確保命中只能來自 embedding，而非關鍵字。
	writeMem(t, d, "cat", "---\nname: cat\ndescription: feline\n---\npurrs")
	writeMem(t, d, "dog", "---\nname: dog\ndescription: canine\n---\nbarks")
	// cat=[1,0]、dog=[0,1]；查詢向量偏向 cat
	WriteVectors(EmbedCachePath(root), map[string][]float32{"cat": {1, 0}, "dog": {0, 1}})

	m := NewMemoryLoader(root)
	out := m.RecallGraph("zzz", 1, fakeEmbedder{vec: []float32{0.9, 0.1}})
	if !strings.Contains(out, "## cat") {
		t.Errorf("語意選種子應命中 cat（查詢向量偏 cat），got:\n%s", out)
	}
	// 對照：無 embedder 時 "zzz" 關鍵字無命中 → 空
	if strings.TrimSpace(m.RecallGraph("zzz", 1, nil)) != "" {
		t.Error("無 embedder 時 zzz 關鍵字應無命中（證明上面的命中來自 embedding）")
	}
}
