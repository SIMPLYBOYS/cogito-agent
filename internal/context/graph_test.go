package context

import (
	"fmt"
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
	nodes, edges, _ := g.Subgraph(seeds, 1, 8)
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
	nodes, _, _ := g.Subgraph([]string{"alpha"}, 2, 2)
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

// 圖也要認【檔名 slug】當節點鍵——那才是這筆記錄的正典識別。
//
// 【為何】整併的 UPDATE/DELETE 用檔名比對、撤回窗靠它事後對回檔案（內容定址）。
// name 只是顯示標題，卻同時被當成 [[link]] 目標——標題一旦不好寫或被改寫，連結就配不到。
// 認 slug 讓「指得到」不依賴標題品質，也給自動推導的邊一個永遠穩定的鍵。
func TestGraph_NodesAddressableByFileSlug(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(slug, name, body string) {
		md := "---\nname: " + name + "\ndescription: d\n---\n" + body
		if err := os.WriteFile(filepath.Join(memDir, slug+".md"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// B 的標題很難當連結目標（正是實庫的情況），但 A 可以用它的檔名 slug 指過去。
	write("mem-aaaa1111", "起點", "從這裡連到 [[mem-bbbb2222]]")
	write("mem-bbbb2222", "外部工具（MCP/API）查不到或未掛載時", "被指向的記錄")

	g := NewMemoryLoader(dir).Graph()

	// slug 要能【轉址】到真記錄，而不是自成一個節點。
	// 斷言真記錄而非「節點存在」：沒有這個機制時 addEdge 也會建一個同名 dangling stub，
	// 只檢查存在會綠得毫無意義（實際踩過這個假綠）。
	n, ok := g.nodes[g.resolve("mem-bbbb2222")]
	if !ok {
		t.Fatal("檔名 slug 轉址不到節點——自動推導的邊會沒有穩定的鍵可用")
	}
	if n.Path == "" || n.Description == "" {
		t.Fatalf("slug 指到的是空殼 stub 而非真記錄：%+v", n)
	}
	if !strings.Contains(n.Body, "被指向的記錄") {
		t.Errorf("slug 指到的節點內容不對：%q", n.Body)
	}
	// 【同一筆記錄只能有一個節點】。曾經把 slug 直接寫成第二個 nodes 鍵，於是 BFS 走訪
	// 兩次、吃掉兩格 budget、子圖裡同一筆印兩遍。別名只轉址，不複製。
	if len(g.nodes) != 2 {
		t.Errorf("兩筆記錄應只有 2 個節點，實際 %d（slug 被當成獨立節點了）", len(g.nodes))
	}
	nodes, edges, _ := g.Subgraph([]string{"起點"}, 1, 8)
	if len(nodes) != 2 {
		t.Errorf("應沿 slug 連結擴張到 2 個節點，實際 %d", len(nodes))
	}
	if len(edges) != 1 {
		t.Errorf("應有 1 條邊，實際 %d", len(edges))
	}
	// 標題仍然可以當鍵，兩種都通。
	if _, ok := g.nodes["外部工具（MCP/API）查不到或未掛載時"]; !ok {
		t.Error("name 也應該還是節點鍵（不能只認 slug）")
	}
}

// 子圖被預算擋下時，輸出必須講出來（DESIGN.md 原則 6：絕不靜默截斷）。
//
// 靜默封頂會讓模型把【部分鄰域】當成完整鄰域——「檢索到的就是我知道的一切」這種錯覺
// 正是這樣來的，而且事後完全查不出來（輸出看起來就是一份正常的子圖）。
func TestRecallGraph_AnnouncesBudgetCap(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 一個星狀圖：中心連出 12 個鄰居，遠超 recallBudget(8)。
	var links string
	for i := 0; i < 12; i++ {
		leaf := fmt.Sprintf("葉%02d", i)
		links += "[[" + leaf + "]] "
		writeMem(t, memDir, fmt.Sprintf("mem-leaf%02d", i),
			"---\nname: "+leaf+"\ndescription: d\n---\n葉節點內容")
	}
	writeMem(t, memDir, "mem-hub", "---\nname: 中心\ndescription: 星狀圖中心\n---\n"+links)

	out := NewMemoryLoader(dir).RecallGraph("中心", 1, nil)
	if out == "" {
		t.Fatal("應撈得到東西")
	}
	if !strings.Contains(out, "上限") {
		t.Errorf("子圖被預算擋下卻沒講——模型會把部分鄰域當成全部。輸出：\n%s", out)
	}
}
