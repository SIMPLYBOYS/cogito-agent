package eval

import (
	"os"
	"path/filepath"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

// mapEmbedder：查詢字串 → 預設向量，讓 embedding 模式可離線、確定性地測。
type mapEmbedder map[string][]float32

func (m mapEmbedder) EmbedQuery(s string) ([]float32, error) { return m[s], nil }

func writeRec(t *testing.T, dir, name, body string) {
	t.Helper()
	doc := "---\nname: " + name + "\ndescription: d\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunMemEval_ThreeModes(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRec(t, d, "cat", "feline animal that purrs")
	writeRec(t, d, "dog", "canine animal that barks")
	writeRec(t, d, "rocket", "space launch vehicle")

	// 向量快取：cat/dog/rocket 三個正交向量
	ctxpkg.WriteVectors(ctxpkg.EmbedCachePath(root), map[string][]float32{
		"cat": {1, 0, 0}, "dog": {0, 1, 0}, "rocket": {0, 0, 1},
	})
	// 查詢向量：feline→近 cat、canine→近 dog（語意，與字面無關）
	emb := mapEmbedder{"feline": {0.9, 0.1, 0}, "canine": {0.1, 0.9, 0}}

	cases := []MemEvalCase{
		{Query: "feline", Expected: []string{"cat"}},
		{Query: "canine", Expected: []string{"dog"}},
	}
	rep := RunMemEval(ctxpkg.NewMemoryLoader(root), cases, emb, 3, 1)

	if rep.Cases != 2 {
		t.Fatalf("應評 2 個查詢，got %d", rep.Cases)
	}
	got := map[string]ModeScore{}
	for _, m := range rep.Modes {
		got[m.Mode] = m
	}
	// keyword："feline"/"canine" 字面就在 body → 兩題都命中
	if got["keyword"].HitAtK != 1.0 {
		t.Errorf("keyword hit@k 應為 1.0，got %v", got["keyword"].HitAtK)
	}
	// embedding：語意選種子也應兩題命中
	if got["embedding"].N != 2 || got["embedding"].HitAtK != 1.0 {
		t.Errorf("embedding 應評 2 題且 hit@k=1.0，got N=%d hit=%v", got["embedding"].N, got["embedding"].HitAtK)
	}
}

func TestRunMemEval_NoEmbedderSkipsMode(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, ".claw", "memory")
	os.MkdirAll(d, 0o755)
	writeRec(t, d, "cat", "feline")
	rep := RunMemEval(ctxpkg.NewMemoryLoader(root), []MemEvalCase{{Query: "feline", Expected: []string{"cat"}}}, nil, 3, 1)
	for _, m := range rep.Modes {
		if m.Mode == "embedding" && m.N != 0 {
			t.Errorf("無 embedder 時 embedding 模式 N 應為 0，got %d", m.N)
		}
	}
}
