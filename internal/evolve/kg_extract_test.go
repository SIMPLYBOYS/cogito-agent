package evolve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

func writeMemRec(t *testing.T, dir, name, body string) {
	t.Helper()
	doc := "---\nname: " + name + "\ndescription: d\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// LLM 抽取必須過濾掉「端點不在提供節點集合」的幻覺邊（ghost），只留端點都存在的。
func TestRelationExtract_FiltersHallucinatedNodes(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMemRec(t, d, "alpha", "alpha 正文")
	writeMemRec(t, d, "beta", "beta 正文")

	fp := &fakeProvider{content: `{"edges":[
		{"from":"alpha","to":"beta","type":"relates-to","confidence":0.9},
		{"from":"alpha","to":"ghost","type":"x","confidence":0.9}
	]}`}
	n, err := NewRelationExtractor(fp, root).Extract(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("應只保留端點都存在的 1 條（ghost 被濾），got %d", n)
	}
	prop := ctxpkg.ReadProposedEdges(root)
	if len(prop) != 1 || prop[0].To != "beta" || prop[0].Source != "llm-extract" {
		t.Errorf("提案邊應只有 alpha→beta(llm-extract)，got %+v", prop)
	}
}

func TestRelationExtract_TooFewNodes(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, ".claw", "memory")
	os.MkdirAll(d, 0o755)
	writeMemRec(t, d, "solo", "only one")
	fp := &fakeProvider{content: `{"edges":[]}`}
	if n, _ := NewRelationExtractor(fp, root).Extract(context.Background()); n != 0 {
		t.Errorf("少於兩節點應抽 0 條，got %d", n)
	}
}
