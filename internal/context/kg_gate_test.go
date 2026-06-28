package context

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeProposed(t *testing.T, root string, edges []StoredEdge) {
	t.Helper()
	dir := filepath.Join(root, ".claw", "kg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, ProposedEdgesFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range edges {
		b, _ := json.Marshal(e)
		f.Write(append(b, '\n'))
	}
}

func TestApplyProposedEdges_Gate(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMem(t, d, "alpha", "---\nname: alpha\n---\nA")
	writeMem(t, d, "beta", "---\nname: beta\n---\nB")
	writeProposed(t, root, []StoredEdge{
		{From: "alpha", To: "beta", Type: "relates-to", Confidence: 0.9}, // 有效
		{From: "alpha", To: "beta", Type: "weak", Confidence: 0.1},       // 低信心 → 拒
		{From: "alpha", To: "ghost", Type: "x", Confidence: 0.9},         // 端點不存在(幻覺) → 拒
		{From: "alpha", To: "alpha", Type: "self", Confidence: 0.9},      // 自環 → 拒
	})

	applied, rejected, err := ApplyProposedEdges(root)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 || rejected != 3 {
		t.Fatalf("應併入 1、拒 3，got applied=%d rejected=%d", applied, rejected)
	}
	g := NewMemoryLoader(root).Graph()
	found := false
	for _, e := range g.out["alpha"] {
		if e.To == "beta" && e.Type == "relates-to" {
			found = true
		}
	}
	if !found {
		t.Error("gate 後 alpha—relates-to→beta 應在圖中")
	}
	if len(ReadProposedEdges(root)) != 0 {
		t.Error("套用後提案檔應清空")
	}
}

func TestApplyProposedEdges_CapPerNode(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMem(t, d, "hub", "---\nname: hub\n---\nH")
	var edges []StoredEdge
	for i := 0; i < maxEdgesPerNode+3; i++ {
		n := fmt.Sprintf("n%d", i)
		writeMem(t, d, n, "---\nname: "+n+"\n---\nx")
		edges = append(edges, StoredEdge{From: "hub", To: n, Type: "link", Confidence: 0.9})
	}
	writeProposed(t, root, edges)

	applied, _, err := ApplyProposedEdges(root)
	if err != nil {
		t.Fatal(err)
	}
	if applied != maxEdgesPerNode {
		t.Errorf("每節點出邊應封頂 %d，got %d", maxEdgesPerNode, applied)
	}
}
