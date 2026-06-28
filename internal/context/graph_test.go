package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupGraph(t *testing.T) *MemoryLoader {
	t.Helper()
	root := t.TempDir()
	d := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMem(t, d, "alpha", "---\nname: alpha\ndescription: 關於 widgets 的主題\n---\nalpha 正文，參見 [[beta]] 與 [[gamma]]，也提到 [[ghost]]。")
	writeMem(t, d, "beta", "---\nname: beta\ndescription: beta 主題\n---\nbeta 正文，連到 [[gamma]]。")
	writeMem(t, d, "gamma", "---\nname: gamma\ndescription: gamma 主題\n---\ngamma 正文，無外連。")
	writeMem(t, d, "delta", "---\nname: delta\ndescription: 孤立主題\n---\ndelta 正文，無連結。")
	return NewMemoryLoader(root)
}

func TestGraph_BuildEdgesAndDangling(t *testing.T) {
	g := setupGraph(t).Graph()
	// 4 真實 + 1 stub(ghost)
	if len(g.nodes) != 5 {
		t.Fatalf("節點數應為 5（含 ghost stub），got %d", len(g.nodes))
	}
	if !isDangling(g.nodes["ghost"]) {
		t.Error("ghost 應為 dangling stub")
	}
	if len(g.out["alpha"]) != 3 { // beta, gamma, ghost
		t.Errorf("alpha 應有 3 條出邊，got %d", len(g.out["alpha"]))
	}
	// gamma 被 alpha 與 beta 指入
	if len(g.in["gamma"]) != 2 {
		t.Errorf("gamma 應有 2 條入邊，got %d", len(g.in["gamma"]))
	}
}

func TestGraph_ParseLinksTyped(t *testing.T) {
	es := parseLinks("see [[plain]] and [[depends-on::core]] end")
	if len(es) != 2 {
		t.Fatalf("應抽出 2 邊，got %d", len(es))
	}
	if es[0].To != "plain" || es[0].Type != "" {
		t.Errorf("generic 邊解析錯: %+v", es[0])
	}
	if es[1].To != "core" || es[1].Type != "depends-on" {
		t.Errorf("typed 邊解析錯: %+v", es[1])
	}
}

func TestGraph_SeedsAndSubgraph(t *testing.T) {
	g := setupGraph(t).Graph()

	seeds := g.Seeds("widgets", 3) // 只有 alpha 的描述含 widgets
	if len(seeds) != 1 || seeds[0] != "alpha" {
		t.Fatalf("種子應為 [alpha]，got %v", seeds)
	}

	// 1 跳：alpha + 直接鄰居 beta/gamma/ghost
	nodes, edges := g.Subgraph(seeds, 1, 8)
	got := map[string]bool{}
	for _, n := range nodes {
		got[n.Name] = true
	}
	for _, want := range []string{"alpha", "beta", "gamma", "ghost"} {
		if !got[want] {
			t.Errorf("子圖應含 %s，got %v", want, got)
		}
	}
	if got["delta"] {
		t.Error("孤立的 delta 不該進子圖")
	}
	// 誘導邊應含 beta→gamma（兩端都在子圖內，展現多跳關係）
	hasBetaGamma := false
	for _, e := range edges {
		if e.From == "beta" && e.To == "gamma" {
			hasBetaGamma = true
		}
	}
	if !hasBetaGamma {
		t.Errorf("誘導子圖應含 beta→gamma 邊，got %+v", edges)
	}
}

func TestGraph_SubgraphBudgetCap(t *testing.T) {
	g := setupGraph(t).Graph()
	nodes, _ := g.Subgraph([]string{"alpha"}, 2, 2)
	if len(nodes) != 2 {
		t.Errorf("budget=2 應只回 2 節點，got %d", len(nodes))
	}
}

func TestRecallGraph_RendersSubgraphWithRelations(t *testing.T) {
	out := setupGraph(t).RecallGraph("widgets", 1, nil)
	if !strings.Contains(out, "## alpha") || !strings.Contains(out, "## gamma") {
		t.Errorf("應渲染子圖節點:\n%s", out)
	}
	if !strings.Contains(out, "### 關係") || !strings.Contains(out, "alpha → beta") {
		t.Errorf("應渲染關係段:\n%s", out)
	}
	if strings.TrimSpace(setupGraph(t).RecallGraph("完全無關鯨魚", 1, nil)) != "" {
		t.Error("無命中應回空字串")
	}
}
