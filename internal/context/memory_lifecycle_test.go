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

// 使用帳本（#2）：被 recall 命中的記錄進帳本（最近使用 + 命中次數），並在淘汰排序中優先保留。
func TestMemoryLoader_RecallRecordsUsageLedger(t *testing.T) {
	root := setupMemory(t)
	m := NewMemoryLoader(root)

	m.Recall("pnpm", 1) // 命中 pnpm 那筆
	u := m.loadUsage()
	if e, ok := u["mem-pnpm.md"]; !ok || e.Hits < 1 || e.LastUsed.IsZero() {
		t.Errorf("命中的記錄應記一次命中（Hits≥1、LastUsed 已設），got %+v (ok=%v)", e, ok)
	}
	// port 未被命中，但首次 loadAll 會把它 seed 進帳本（Hits=0，凍住 mtime 免疫日後外部碰檔）
	if e, ok := u["mem-port.md"]; !ok || e.Hits != 0 {
		t.Errorf("未命中的記錄應被 seed（存在且 Hits=0），got %+v (ok=%v)", e, ok)
	}
	// 再命中一次 → Hits 累加（供日後 LFU/#3）
	m.Recall("pnpm", 1)
	if m.loadUsage()["mem-pnpm.md"].Hits < 2 {
		t.Error("重複命中應累加 Hits")
	}
}

// #2 的賣點：帳本免疫外部碰檔。備份/rsync/編輯器把某筆記憶的【檔案 mtime】改新，不該讓它在淘汰排序
// 中贏過「帳本記錄為剛被 agent 用到」的那筆——這正是舊版（純看 mtime）會誤判、而帳本要修掉的。
func TestMemoryLoader_LedgerBeatsExternalMtimeTouch(t *testing.T) {
	root := setupMemory(t)
	memDir := filepath.Join(root, ".claw", "memory")
	m := NewMemoryLoader(root)

	m.Recall("pnpm", 1) // agent 真的用到 pnpm 那筆 → 進帳本

	// 模擬外部工具「碰」了 port 那筆的檔案 mtime（把它改成未來，比帳本還新）
	future := time.Now().Add(24 * time.Hour)
	if err := os.Chtimes(filepath.Join(memDir, "mem-port.md"), future, future); err != nil {
		t.Fatal(err)
	}

	// keep=1 淘汰：該保留「agent 真的用到」的 pnpm，歸檔被外部碰檔的 port
	archived := m.Prune(1)
	for _, a := range archived {
		if a == "mem-pnpm.md" {
			t.Fatal("被 agent 用到的 pnpm 不該被淘汰（帳本應勝過外部 mtime 污染）")
		}
	}
	if _, err := os.Stat(filepath.Join(memDir, "mem-pnpm.md")); err != nil {
		t.Error("pnpm 應仍在 memory/（帳本保護）")
	}
}
