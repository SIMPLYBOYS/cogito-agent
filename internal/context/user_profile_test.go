package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func profileDir(t *testing.T) (root, memDir string) {
	t.Helper()
	root = t.TempDir()
	memDir = filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, memDir
}

// 畫像與一般記憶是刻意的兩級待遇：畫像放正文（每輪常駐），其餘只放索引（正文等 recall）。
func TestUserProfile_BodyResidentIndexNot(t *testing.T) {
	root, memDir := profileDir(t)
	writeMem(t, memDir, "u-lang",
		"---\nname: 語言偏好\ndescription: 使用者要繁體中文\ntags: [user]\n---\n一律用繁體中文回覆，不要簡體。")
	writeMem(t, memDir, "m-pnpm",
		"---\nname: 用-pnpm\ndescription: 本專案用 pnpm\ntags: [依賴]\n---\n安裝請 `pnpm install`。")

	idx := NewMemoryLoader(root).LoadIndex()

	if !strings.Contains(idx, "關於使用者") {
		t.Fatalf("缺畫像區塊:\n%s", idx)
	}
	if !strings.Contains(idx, "一律用繁體中文回覆") {
		t.Errorf("畫像應含正文:\n%s", idx)
	}
	if strings.Contains(idx, "pnpm install") {
		t.Errorf("一般記憶不該進正文:\n%s", idx)
	}
	// 畫像不該又出現在索引清單裡（重複＝白花 token）
	if n := strings.Count(idx, "語言偏好"); n != 1 {
		t.Errorf("畫像出現 %d 次，應只在畫像區塊出現一次:\n%s", n, idx)
	}
}

func TestUserProfile_TagIsCaseInsensitive(t *testing.T) {
	root, memDir := profileDir(t)
	writeMem(t, memDir, "u-x",
		"---\nname: 偏好\ndescription: d\ntags: [User, 慣例]\n---\n正文內容ABC")
	if idx := NewMemoryLoader(root).LoadIndex(); !strings.Contains(idx, "正文內容ABC") {
		t.Errorf("大寫 User 也該算畫像:\n%s", idx)
	}
}

// 常駐＝每輪固定開銷，必須封頂；且寧可整條不放，也不要把「不要 X」截成半句。
func TestUserProfile_Bounded(t *testing.T) {
	root, memDir := profileDir(t)
	body := strings.Repeat("偏好描述", 100) // 400 runes/條
	for i := range 20 {
		writeMem(t, memDir, fmt.Sprintf("u-%02d", i),
			fmt.Sprintf("---\nname: p%02d\ndescription: d\ntags: [user]\n---\n%s", i, body))
	}
	idx := NewMemoryLoader(root).LoadIndex()

	if n := strings.Count(idx, body); n > maxProfileEntries {
		t.Errorf("條數 %d 超過上限 %d", n, maxProfileEntries)
	}
	if n := len([]rune(idx)); n > maxProfileRunes+1000 { // +1000：標題/導言/省略行的餘裕
		t.Errorf("畫像總長 %d runes 明顯超過額度 %d", n, maxProfileRunes)
	}
	if !strings.Contains(idx, "超出常駐額度") {
		t.Errorf("超額時要講清楚還有幾條、可用 recall 取:\n%s", idx)
	}
	if strings.Contains(idx, "偏好描述偏好") && strings.Count(idx, body) == 0 {
		t.Error("不該出現半截正文")
	}
}

// 畫像是凍結的 prompt 前綴：順序每次都要一樣，否則 prefix cache 全打掉。
func TestUserProfile_StableOrder(t *testing.T) {
	root, memDir := profileDir(t)
	for _, n := range []string{"c", "a", "b"} {
		writeMem(t, memDir, "u-"+n,
			fmt.Sprintf("---\nname: %s\ndescription: d\ntags: [user]\n---\nbody-%s", n, n))
	}
	idx := NewMemoryLoader(root).LoadIndex()
	ia, ib, ic := strings.Index(idx, "body-a"), strings.Index(idx, "body-b"), strings.Index(idx, "body-c")
	if !(ia < ib && ib < ic) {
		t.Errorf("畫像應依名稱穩定排序，got a=%d b=%d c=%d", ia, ib, ic)
	}
}

// 只有畫像、沒有其他記憶時不該冒出一個空的索引區塊。
func TestUserProfile_OnlyProfileNoEmptyIndex(t *testing.T) {
	root, memDir := profileDir(t)
	writeMem(t, memDir, "u-only",
		"---\nname: 唯一\ndescription: d\ntags: [user]\n---\nbody-only")
	if idx := NewMemoryLoader(root).LoadIndex(); strings.Contains(idx, "長期記憶索引") {
		t.Errorf("沒有一般記憶就不該印索引區塊:\n%s", idx)
	}
}
