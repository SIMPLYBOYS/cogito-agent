package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 多 goroutine 同時 ingest 同一 root（＝多 session 併發寫知識層）：知識層鎖應序列化寫入，
// 使 edges.jsonl 每行都是完整合法 JSON（無交錯損毀）。配 -race 一起跑。
func TestKnowledgeLock_ConcurrentIngestNoCorruption(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.md"), []byte("# A\n見 [B](b.md) 與 [[c]]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.md"), []byte("# B"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewMemoryLoader(root)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = m.IngestDir(src)
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(root, ".claw", "kg", "edges.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if ln == "" {
			continue
		}
		var se StoredEdge
		if json.Unmarshal([]byte(ln), &se) != nil || se.From == "" {
			t.Fatalf("併發寫造成損毀行: %q", ln)
		}
		lines++
	}
	if lines == 0 {
		t.Error("應有邊（dedup 後仍至少 a→b、a→c）")
	}
}
