package context

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// KG 檢索預設值：種子數、子圖節點上限。封頂避免子圖爆 context。
const (
	recallSeeds  = 3
	recallBudget = 8
)

var linkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// Edge 是圖的一條有向邊。Type="" 是 generic [[link]]；typed edge（如 depends-on）留待 Stage 2 的 LLM 抽取。
type Edge struct {
	From string
	To   string
	Type string
}

// Graph 把現有記憶記錄當成圖：節點=記錄、邊=正文 [[links]]。載入時在記憶體建鄰接表（out/in 雙向）。
type Graph struct {
	nodes map[string]MemoryRecord
	out   map[string][]Edge
	in    map[string][]Edge
	alias map[string]string // 檔名 slug → 節點鍵（name）。只轉址，不複製節點。
}

// parseLinks 從正文抽 [[target]] 或 [[type::target]]（後者為 Stage 2 typed edge 預留）。
func parseLinks(body string) []Edge {
	var edges []Edge
	for _, m := range linkRe.FindAllStringSubmatch(body, -1) {
		raw := strings.TrimSpace(m[1])
		typ, target := "", raw
		if i := strings.Index(raw, "::"); i >= 0 {
			typ, target = strings.TrimSpace(raw[:i]), strings.TrimSpace(raw[i+2:])
		}
		if target != "" {
			edges = append(edges, Edge{To: target, Type: typ})
		}
	}
	return edges
}

// Graph 從現有記錄建圖。懸空連結（指向不存在的節點）建成 stub 節點（標 dangling），保留「被引用但尚未撰寫」信號。
func (m *MemoryLoader) Graph() *Graph {
	recs := m.loadAll()
	g := &Graph{
		nodes: make(map[string]MemoryRecord, len(recs)),
		out:   make(map[string][]Edge),
		in:    make(map[string][]Edge),
		alias: make(map[string]string),
	}
	for _, r := range recs {
		g.nodes[r.Name] = r
		// 檔名 slug（mem-xxxxxxxx）登記成【別名】而不是第二個節點。
		//
		// 【為何是別名】slug 才是這筆記錄的正典 ID（整併用它比對、撤回窗靠它對回檔案），
		// 所以連結與推導邊都該指得到它。但若直接 g.nodes[slug]=r，同一筆記錄就變成兩個
		// 節點——BFS 的 visited 以鍵為單位，同一筆會被走訪兩次、吃掉兩格 budget、
		// 在子圖裡印兩遍。實測踩過。別名只做鍵的轉址，節點永遠一份。
		if slug := recordSlug(r); slug != "" && slug != r.Name {
			g.alias[slug] = r.Name
		}
	}
	for _, r := range recs {
		for _, e := range parseLinks(r.Body) {
			e.From = r.Name
			g.addEdge(e)
		}
	}
	// 合併外部/typed 邊（.claw/kg/edges.jsonl）：md ingest 的結構邊與 LLM 抽取的 typed 邊都存這裡。
	for _, e := range readEdgesFile(filepath.Join(m.workDir, ".claw", "kg", "edges.jsonl")) {
		g.addEdge(e)
	}
	return g
}

// recordSlug 回一筆記錄的檔名（去掉 .md）——內容定址的正典 ID。無 Path（stub 節點）回空字串。
func recordSlug(r MemoryRecord) string {
	if r.Path == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(r.Path), ".md")
}

// resolve 把任意鍵（節點名或檔名 slug）轉成正典節點鍵。不認得就原樣回傳
// ——讓呼叫端照舊走「建 dangling stub」那條路。
func (g *Graph) resolve(key string) string {
	if _, ok := g.nodes[key]; ok {
		return key
	}
	if canon, ok := g.alias[key]; ok {
		return canon
	}
	return key
}

// addEdge 加一條邊到鄰接表；自環略過、指向不存在節點則建 dangling stub。
func (g *Graph) addEdge(e Edge) {
	// 先把兩端轉成正典鍵：推導邊用 slug 當端點，不轉的話會各自建出 dangling stub，
	// 於是圖裡同時有「真記錄」與「同一筆的空殼」，而邊只連到空殼那個。
	e.From, e.To = g.resolve(e.From), g.resolve(e.To)
	if e.From == "" || e.To == "" || e.From == e.To {
		return
	}
	g.out[e.From] = append(g.out[e.From], e)
	g.in[e.To] = append(g.in[e.To], e)
	if _, ok := g.nodes[e.To]; !ok {
		g.nodes[e.To] = MemoryRecord{Name: e.To, Description: "（尚未撰寫，被引用）", Tags: []string{"dangling"}}
	}
}

// StoredEdge 是 edges.jsonl 的一行：帶 type/confidence/source（供 ingest 與 gated LLM 抽取共用、可審計）。
type StoredEdge struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Type       string  `json:"type,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Source     string  `json:"source,omitempty"`
}

func readEdgesFile(path string) []Edge {
	stored := readStoredEdges(path)
	out := make([]Edge, 0, len(stored))
	for _, se := range stored {
		out = append(out, Edge{From: se.From, To: se.To, Type: se.Type})
	}
	return out
}

// readStoredEdges 讀 edges jsonl 的完整 StoredEdge（含 confidence/source，供 gate 用）。
func readStoredEdges(path string) []StoredEdge {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []StoredEdge
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var se StoredEdge
		if json.Unmarshal([]byte(line), &se) == nil && se.From != "" && se.To != "" {
			out = append(out, se)
		}
	}
	return out
}

// Seeds 用關鍵字評分挑種子節點（排除 stub）。複用 memory.go 的 tokenize/scoreRecord。
func (g *Graph) Seeds(query string, n int) []string {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}
	type sc struct {
		name  string
		score int
	}
	var ranked []sc
	for name, r := range g.nodes {
		if isDangling(r) {
			continue
		}
		if s := scoreRecord(r, terms); s > 0 {
			ranked = append(ranked, sc{name, s})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].name < ranked[j].name // 同分時穩定排序
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

func isDangling(r MemoryRecord) bool {
	for _, t := range r.Tags {
		if t == "dangling" {
			return true
		}
	}
	return false
}

// neighbors 回傳節點的無向鄰居（out 的 To + in 的 From）——關係兩向都算相關。
func (g *Graph) neighbors(name string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(x string) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	for _, e := range g.out[name] {
		add(e.To)
	}
	for _, e := range g.in[name] {
		add(e.From)
	}
	return out
}

// Subgraph 從種子 BFS 無向擴張 hops 跳，總節點封頂 budget（就近優先）；回傳節點（BFS 序）與誘導子圖的邊。
// 第三個回傳值＝【是否因為 budget 而停下】。呼叫端要據此告訴使用者/模型「這不是全部」
// ——靜默截斷會讓人把部分鄰域當成完整鄰域（DESIGN.md 原則 6）。
func (g *Graph) Subgraph(seeds []string, hops, budget int) ([]MemoryRecord, []Edge, bool) {
	if budget <= 0 {
		budget = recallBudget
	}
	visited := map[string]bool{}
	depth := map[string]int{}
	var order, queue []string
	for _, s := range seeds {
		s = g.resolve(s) // 種子也可能是 slug
		if _, ok := g.nodes[s]; ok && !visited[s] {
			visited[s] = true
			depth[s] = 0
			order = append(order, s)
			queue = append(queue, s)
		}
	}
	truncated := false
	for len(queue) > 0 && len(order) < budget {
		cur := queue[0]
		queue = queue[1:]
		if depth[cur] >= hops {
			continue
		}
		for _, nb := range g.neighbors(cur) {
			if visited[nb] {
				continue
			}
			if len(order) >= budget {
				truncated = true // 還有沒展開的鄰居，是預算把我們擋下來的
				continue
			}
			visited[nb] = true
			depth[nb] = depth[cur] + 1
			order = append(order, nb)
			queue = append(queue, nb)
		}
	}
	nodes := make([]MemoryRecord, 0, len(order))
	for _, name := range order {
		nodes = append(nodes, g.nodes[name])
	}
	// 誘導子圖的邊：兩端都在子圖內，去重。
	seenEdge := map[Edge]bool{}
	var edges []Edge
	for name := range visited {
		for _, e := range g.out[name] {
			if visited[e.To] && !seenEdge[e] {
				seenEdge[e] = true
				edges = append(edges, e)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return nodes, edges, truncated
}

// RenderSubgraph 把子圖序列化給 LLM：各節點正文 + 一段明確的關係列表（讓模型在關係上做多跳推理）。
func RenderSubgraph(nodes []MemoryRecord, edges []Edge) string {
	if len(nodes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, n := range nodes {
		if i > 0 {
			b.WriteString("\n---\n")
		}
		tag := ""
		if len(n.Tags) > 0 {
			tag = "  [" + strings.Join(n.Tags, ", ") + "]"
		}
		fmt.Fprintf(&b, "## %s%s\n", n.Name, tag)
		if strings.TrimSpace(n.Body) == "" {
			b.WriteString("（尚未撰寫的節點，被其他記憶引用）\n")
		} else {
			b.WriteString(n.Body + "\n")
		}
	}
	if len(edges) > 0 {
		b.WriteString("\n### 關係\n")
		for _, e := range edges {
			rel := "→"
			if e.Type != "" {
				rel = "—" + e.Type + "→"
			}
			fmt.Fprintf(&b, "- %s %s %s\n", e.From, rel, e.To)
		}
	}
	return b.String()
}
