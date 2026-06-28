package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 索引封頂：記憶多於 maxIndexEntries 時，常駐索引只列上限條數 + 提示其餘可 recall。
func TestMemoryLoader_IndexCapsWithOverflowNote(t *testing.T) {
	root := t.TempDir()
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxIndexEntries+5; i++ {
		writeMem(t, memDir, fmt.Sprintf("m%02d", i),
			fmt.Sprintf("---\nname: 記憶%02d\ndescription: 摘要%02d\n---\n正文%02d", i, i, i))
	}
	idx := NewMemoryLoader(root).LoadIndex()
	if got := strings.Count(idx, "- **"); got != maxIndexEntries {
		t.Errorf("索引應封頂在 %d 條，got %d", maxIndexEntries, got)
	}
	if !strings.Contains(idx, "另有 5 條") {
		t.Errorf("應提示其餘條數，got:\n%s", idx)
	}
}

// 遺忘：超過 keep 上限時，最久未用（mtime 最舊）的歸檔到 memory-archive（可復原），新的保留。
func TestMemoryLoader_PruneArchivesOldest(t *testing.T) {
	root := t.TempDir()
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-100 * time.Hour)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("m%d", i)
		writeMem(t, memDir, name, "---\nname: "+name+"\n---\nbody")
		mt := base.Add(time.Duration(i) * time.Hour) // i 越大越新
		if err := os.Chtimes(filepath.Join(memDir, name+".md"), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	archived := NewMemoryLoader(root).Prune(3)
	if len(archived) != 2 {
		t.Fatalf("超出 keep=3 應歸檔 2 筆，got %d (%v)", len(archived), archived)
	}
	for _, old := range []string{"m0.md", "m1.md"} { // 最舊兩筆
		if _, err := os.Stat(filepath.Join(memDir, old)); !os.IsNotExist(err) {
			t.Errorf("%s 應已從 memory 移走", old)
		}
		if _, err := os.Stat(filepath.Join(root, ".claw", "memory-archive", old)); err != nil {
			t.Errorf("%s 應在 memory-archive，err=%v", old, err)
		}
	}
	if _, err := os.Stat(filepath.Join(memDir, "m4.md")); err != nil {
		t.Error("最新記錄不該被歸檔")
	}
	if got := NewMemoryLoader(root).Recall("body", 10); len(got) != 3 {
		t.Errorf("歸檔後 recall 應只看到 3 筆（archive 是兄弟目錄、不在 loadAll 範圍），got %d", len(got))
	}
}

// LRU：被 recall 命中的記錄 mtime 應更新，使其在淘汰/索引排序中優先保留。
func TestMemoryLoader_RecallTouchesMtime(t *testing.T) {
	root := setupMemory(t)
	memDir := filepath.Join(root, ".claw", "memory")
	old := time.Now().Add(-48 * time.Hour)
	for _, n := range []string{"mem-pnpm.md", "mem-port.md"} {
		if err := os.Chtimes(filepath.Join(memDir, n), old, old); err != nil {
			t.Fatal(err)
		}
	}
	NewMemoryLoader(root).Recall("pnpm", 1) // 命中 pnpm 那筆 → 觸碰其 mtime
	pnpmInfo, _ := os.Stat(filepath.Join(memDir, "mem-pnpm.md"))
	portInfo, _ := os.Stat(filepath.Join(memDir, "mem-port.md"))
	if !pnpmInfo.ModTime().After(portInfo.ModTime()) {
		t.Error("被 recall 命中的記錄 mtime 應更新為較新（LRU 觸碰）")
	}
}
