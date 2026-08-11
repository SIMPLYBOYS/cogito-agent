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

	// 只封字數，不另設條數上限——條數先前才是真正卡住的那道閘（實測 12 條只用掉 672 字，
	// 2000 字的預算浪費三分之二），而成本本來就按字算。
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

// 畫像是凍結的 prompt 前綴：順序每次都要一樣，否則 prefix cache 全打掉。排序鍵用 recorded
// （寫進檔案後不再變動），不是 usedAt——後者會因為被 recall 而變，順序就不穩了。
//
// 為何不再依名稱：同樣穩定，但選出來的是【字典序前 N 名】，而 name 是 description 砍到前 24 字。
// 實測 54 條畫像：「你…」開頭的全進、「使用者…」開頭的全滅，只因為「你」的碼位比「使」小。
func TestUserProfile_NewestFirstAndStable(t *testing.T) {
	root, memDir := profileDir(t)
	// 刻意讓「寫入順序」「檔名順序」「時間順序」三者不一致，才驗得出排的是時間。
	for _, c := range []struct{ slug, ts string }{
		{"u-a", "2026-08-09T10:00:00+08:00"},
		{"u-c", "2026-08-11T10:00:00+08:00"},
		{"u-b", "2026-08-10T10:00:00+08:00"},
	} {
		writeMem(t, memDir, c.slug,
			fmt.Sprintf("---\nname: %s\ndescription: d\ntags: [user]\nrecorded: %s\n---\nbody-%s",
				c.slug, c.ts, c.slug))
	}
	idx := NewMemoryLoader(root).LoadIndex()
	ia, ib, ic := strings.Index(idx, "body-u-a"), strings.Index(idx, "body-u-b"), strings.Index(idx, "body-u-c")
	if !(ic < ib && ib < ia) {
		t.Errorf("最近寫的該排最前，got c=%d b=%d a=%d\n%s", ic, ib, ia, idx)
	}
	if again := NewMemoryLoader(root).LoadIndex(); again != idx {
		t.Error("同一份記憶庫兩次載入結果不同——前綴快取會被打掉")
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
