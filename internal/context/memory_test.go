package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMem(t *testing.T, dir, slug, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupMemory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMem(t, memDir, "mem-pnpm",
		"---\nname: 用-pnpm\ndescription: 本專案用 pnpm 而非 npm 裝依賴\ntags: [依賴, 建置]\n---\n安裝請 `pnpm install`，CI 也是。")
	writeMem(t, memDir, "mem-port",
		"---\nname: 埠衝突\ndescription: 起本地 server 前先查埠是否被占\ntags: [除錯, 環境]\n---\n8765 常被占，先 lsof 檢查再起。")
	return root
}

// LoadIndex 只放元資料（名稱+描述+標籤），不放正文——正文留給 recall 按需取。
func TestMemoryLoader_IndexHasMetaNotBody(t *testing.T) {
	idx := NewMemoryLoader(setupMemory(t)).LoadIndex()
	if !strings.Contains(idx, "用-pnpm") || !strings.Contains(idx, "pnpm 而非 npm") {
		t.Errorf("索引應含名稱與描述，got:\n%s", idx)
	}
	if strings.Contains(idx, "pnpm install") || strings.Contains(idx, "lsof") {
		t.Error("索引不應含正文（pnpm install / lsof 屬於 body）")
	}
	if !strings.Contains(idx, "依賴") {
		t.Error("索引應帶標籤")
	}
}

// Recall 依關鍵字評分，回最相關的記錄正文；中文 bigram 檢索也要命中。
func TestMemoryLoader_RecallRanksAndMatchesCJK(t *testing.T) {
	m := NewMemoryLoader(setupMemory(t))

	got := m.Recall("pnpm 依賴", 2)
	if len(got) == 0 || !strings.Contains(got[0].Body, "pnpm install") {
		t.Fatalf("最相關應為 pnpm 那筆，got %+v", got)
	}

	// 純中文查詢（無空白）走 bigram：「埠」相關應命中 port 那筆
	cjk := m.Recall("本地埠衝突", 1)
	if len(cjk) != 1 || !strings.Contains(cjk[0].Body, "lsof") {
		t.Fatalf("中文 bigram 檢索應命中埠那筆，got %+v", cjk)
	}

	if none := m.Recall("完全無關的鯨魚主題", 3); len(none) != 0 {
		t.Errorf("不相關查詢應回空，got %+v", none)
	}
}
