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

// 記憶的 name 是 description 砍到前 24 字（evolve.writeMemoryRecord），所以索引整條都在自我重複：
// 「- **實價登錄單價欄位為每平方公尺，換算每坪需乘以 3**：實價登錄單價欄位為每平方公尺，換算每坪需乘以 3.305785。」
// 索引每一輪都常駐，這是固定的白付。name 不是前綴時仍要照印——那時它是真的標題。
func TestMemoryLoader_IndexDropsNameWhenItPrefixesDescription(t *testing.T) {
	root := t.TempDir()
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(slug, name, desc string) {
		writeMem(t, memDir, slug, "---\nname: "+name+"\ndescription: "+desc+"\n---\n正文")
	}
	write("mem-a", "實價登錄單價欄位為每平方公尺，換算每坪需乘以 3", "實價登錄單價欄位為每平方公尺，換算每坪需乘以 3.305785。")
	write("mem-b", "部署流程", "上線前要先跑 make verify。")

	idx := NewMemoryLoader(root).LoadIndex()
	if strings.Contains(idx, "**實價登錄單價欄位為每平方公尺，換算每坪需乘以 3**") {
		t.Errorf("name 是 description 的前綴，不該再印一次：\n%s", idx)
	}
	if !strings.Contains(idx, "換算每坪需乘以 3.305785。") {
		t.Errorf("完整描述不可丟：\n%s", idx)
	}
	if !strings.Contains(idx, "**部署流程**：上線前要先跑 make verify。") {
		t.Errorf("真的是標題的 name 要照印：\n%s", idx)
	}
}

// trigger 欄位：「什麼情況該想起我」與內容分離（devin-actions #1）。
//
// 為什麼需要：實庫 14 筆記憶 11 筆 hits:0。「換算每坪乘 3.305785」的觸發時機是
// 「使用者問房價/坪數」，但那幾個字【不在內容裡】——關鍵字比對天生撈不到。
// 技能索引早就是這個原則（description = 何時用），記憶層一直缺。
func TestRecall_TriggerFieldMatchesWhenContentDoesNot(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMem(t, memDir, "mem-pyeong",
		"---\nname: 單價換算\ndescription: 每平方公尺乘 3.305785\ntrigger: 房價 坪數 實價登錄\n---\n每平方公尺單價乘 3.305785 得每坪單價")
	writeMem(t, memDir, "mem-other",
		"---\nname: 無關記錄\ndescription: 別的事\n---\n完全無關的內容")

	out := NewMemoryLoader(dir).RecallGraph("坪數", 1, nil)
	if !strings.Contains(out, "3.305785") {
		t.Errorf("查詢詞只出現在 trigger、不在內容——應該撈得到。輸出：%q", out)
	}
	if strings.Contains(out, "無關記錄") {
		t.Errorf("無關記錄不該被撈出來：%q", out)
	}
}

// 沒有 trigger 的舊記錄行為完全不變（向後相容是完成條件之一）。
func TestRecall_NoTriggerUnchanged(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMem(t, memDir, "mem-plain",
		"---\nname: 一般記錄\ndescription: 講編碼\n---\n遇到編碼錯先設 UTF-8")
	out := NewMemoryLoader(dir).RecallGraph("編碼", 1, nil)
	if !strings.Contains(out, "UTF-8") {
		t.Errorf("舊格式記錄應照常撈到：%q", out)
	}
}
