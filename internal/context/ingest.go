package context

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 結構抽取：把 markdown 的「檔案連結」[text](other.md) 也當成邊（[[wikilink]] 由 parseLinks 處理）。
var mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+\.md)[^)]*\)`)

// IngestDir 把一個目錄的 .md 結構式 ingest 成知識圖譜的節點與邊（確定性、不呼叫 LLM、不花錢）：
//   - 每個 .md → 一筆記憶記錄（node id = 相對路徑去副檔名；tags 標 source:ingest 以與人工記憶區分）
//   - 檔內的 [text](x.md) 與 [[x]] → 邊，寫進 .claw/kg/edges.jsonl（去重）
//
// 之後 recall 的子圖檢索（Stage 1）就能跨這些 ingested 檔案做多跳關係檢索。LLM typed 關係抽取是 Stage 2b。
func (m *MemoryLoader) IngestDir(srcDir string) (nodes, edges int, err error) {
	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()
	memDir := m.dir()
	if e := os.MkdirAll(memDir, 0o755); e != nil {
		return 0, 0, e
	}
	var stored []StoredEdge
	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return err
		}
		rel, e := filepath.Rel(srcDir, path)
		if e != nil {
			return e
		}
		id := nodeID(rel)
		content, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		body := string(content)
		if e := writeIngestRecord(memDir, id, body); e != nil {
			return e
		}
		nodes++
		for _, to := range structuralLinks(rel, body) {
			stored = append(stored, StoredEdge{From: id, To: to, Type: "link", Confidence: 1.0, Source: "ingest:" + rel})
		}
		return nil
	})
	if walkErr != nil {
		return nodes, 0, walkErr
	}
	n, e := appendEdges(filepath.Join(m.workDir, ".claw", "kg", "edges.jsonl"), stored)
	return nodes, n, e
}

// nodeID 由相對路徑產生節點 id：去 .md、路徑分隔轉「/」（穩定、可讀，作 [[連結]] 目標）。
func nodeID(rel string) string {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), ".md")
	return rel
}

// structuralLinks 抽出該檔指向的其他節點 id：markdown 檔連結（相對於該檔解析）+ [[wikilink]]。
func structuralLinks(rel, body string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	dir := filepath.Dir(rel)
	for _, m := range mdLinkRe.FindAllStringSubmatch(body, -1) {
		target := strings.TrimSpace(m[1])
		if strings.Contains(target, "://") {
			continue // 外部 URL 不算節點
		}
		add(nodeID(filepath.Join(dir, target))) // 相對該檔解析
	}
	for _, e := range parseLinks(body) {
		add(e.To) // [[wikilink]] 直接當 id
	}
	return out
}

func writeIngestRecord(memDir, id, body string) error {
	desc := firstMeaningfulLine(body)
	doc := fmt.Sprintf("---\nname: %s\ndescription: %s\ntags: [source:ingest]\n---\n%s\n", id, desc, body)
	return os.WriteFile(filepath.Join(memDir, slug(id)+".md"), []byte(doc), 0o644)
}

// slug 把 node id 轉成安全檔名（/ 與空白 → __）。
func slug(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == filepath.Separator {
			return '_'
		}
		return r
	}, id)
}

func firstMeaningfulLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		line = strings.TrimSpace(line)
		if line != "" {
			if r := []rune(line); len(r) > 80 {
				return string(r[:80])
			}
			return line
		}
	}
	return "(ingested document)"
}

// appendEdges 把邊追加到 edges.jsonl，依 (from,type,to) 去重（不重複寫已存在的邊）。回傳新增條數。
func appendEdges(path string, edges []StoredEdge) (int, error) {
	if len(edges) == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	existing := map[string]bool{}
	for _, e := range readEdgesFile(path) {
		existing[e.From+"\x00"+e.Type+"\x00"+e.To] = true
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	added := 0
	for _, e := range edges {
		key := e.From + "\x00" + e.Type + "\x00" + e.To
		if existing[key] {
			continue
		}
		existing[key] = true
		b, _ := json.Marshal(e)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}
