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

// 真實多跳語料（testdata/mem_multihop：12 個互連服務節點 + 12 題標註，半數多跳）：
// 證明「keyword+KG 在多跳查詢上贏過純 keyword」——多跳題的答案節點與查詢零字面重疊，
// keyword 撈不到（Seeds 只回 score>0），只有沿 [[link]] 擴張的 KG 撈得到。這是 KG 勝 RAG 的量化證據。
func TestRunMemEval_MultiHop_KGBeatsKeyword(t *testing.T) {
	root := "testdata/mem_multihop"
	cases, err := LoadMemLabels(filepath.Join(root, "labels.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rep := RunMemEval(ctxpkg.NewMemoryLoader(root), cases, nil, 3, 1)
	got := map[string]ModeScore{}
	for _, m := range rep.Modes {
		got[m.Mode] = m
	}
	kw, kg := got["keyword"].HitAtK, got["keyword+kg"].HitAtK
	if kg <= kw {
		t.Errorf("多跳語料上 keyword+kg(%.2f) 應【嚴格優於】keyword(%.2f)", kg, kw)
	}
	if kg != 1.0 {
		t.Errorf("keyword+kg 應命中全部（含多跳），got %.2f", kg)
	}
	if kw >= 1.0 { // keyword 若滿分表示語料沒真正考多跳（護欄：改壞語料會被抓到）
		t.Errorf("keyword 不該滿分（多跳題應漏），got %.2f", kw)
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
