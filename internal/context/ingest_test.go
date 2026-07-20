package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestDir_NodesAndEdges(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "docs-src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// guide.md 連到 sub/api.md（markdown 檔連結）與 [[glossary]]（wikilink）
	os.WriteFile(filepath.Join(src, "guide.md"),
		[]byte("# 安裝指南\n\n關於 widgets 的設定，詳見 [API](sub/api.md) 與 [[glossary]]。"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "api.md"),
		[]byte("# API 參考\n\nwidgets 的 API。"), 0o644)

	m := NewMemoryLoader(root)
	nodes, edges, err := m.IngestDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if nodes != 2 {
		t.Errorf("應 ingest 2 個節點，got %d", nodes)
	}
	if edges != 2 { // guide→sub/api, guide→glossary
		t.Errorf("應有 2 條新邊，got %d", edges)
	}

	// 記錄落地且標 source:ingest
	recB, _ := os.ReadFile(filepath.Join(root, ".claw", "memory", "guide.md"))
	rec := string(recB)
	if !strings.Contains(rec, "name: guide") || !strings.Contains(rec, "source:ingest") {
		t.Errorf("ingested 記錄應有 name 與 source:ingest tag:\n%s", rec)
	}

	// edges.jsonl 應含 guide→sub/api 的 link 邊
	ejB, _ := os.ReadFile(filepath.Join(root, ".claw", "kg", "edges.jsonl"))
	ej := string(ejB)
	if !strings.Contains(ej, `"from":"guide"`) || !strings.Contains(ej, `"to":"sub/api"`) {
		t.Errorf("edges.jsonl 應含 guide→sub/api:\n%s", ej)
	}

	// Graph 合併 edges.jsonl：guide 應連到 sub/api
	g := m.Graph()
	foundEdge := false
	for _, e := range g.out["guide"] {
		if e.To == "sub/api" {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Error("Graph 應從 edges.jsonl 合併出 guide→sub/api 邊")
	}

	// recall 跨檔案多跳：查 widgets → 命中 guide，子圖含鄰居 sub/api
	out := m.RecallGraph("widgets", 1, nil)
	if !strings.Contains(out, "## guide") || !strings.Contains(out, "## sub/api") {
		t.Errorf("recall 子圖應跨 ingested 檔案含 guide 與 sub/api:\n%s", out)
	}
}

func TestAppendEdges_Dedup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edges.jsonl")
	e := []StoredEdge{{From: "a", To: "b", Type: "link"}}
	if n, _ := appendEdges(path, e); n != 1 {
		t.Fatalf("首次應寫 1 條，got %d", n)
	}
	if n, _ := appendEdges(path, e); n != 0 {
		t.Errorf("重複邊應去重、寫 0 條，got %d", n)
	}
}
