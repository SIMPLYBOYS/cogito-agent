package context

import (
	"os"
	"path/filepath"
	"testing"
)

// 同任務的記錄要互相連上，端點用【檔名 slug】。
//
// 這條守著 #1 的整個價值主張：圖原本只有兩種邊——人工 [[links]]（實庫 0 條）與 LLM 抽取
// （要錢、要人審）。推導邊是第三條路：確定性、零成本、可重跑。端點必須是 slug 而不是 name，
// 因為 name 是顯示標題、會被改寫；slug 是內容定址的正典 ID。
func TestDeriveEdges_SameTaskLinked(t *testing.T) {
	root := t.TempDir()
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 兩種 provenance 格式都要吃：新格式與舊格式共存於實庫。
	writeMem(t, memDir, "mem-aaa", "---\nname: 甲\ndescription: d\n---\n學習一\n\n〔來源 provenance〕由「慣例」反思、於 t 從任務「查房價」沉澱。")
	writeMem(t, memDir, "mem-bbb", "---\nname: 乙\ndescription: d\n---\n學習二\n\n（來源：任務「查房價」）")
	writeMem(t, memDir, "mem-ccc", "---\nname: 丙\ndescription: d\n---\n學習三\n\n（來源：任務「別的任務」）")
	writeMem(t, memDir, "mem-ddd", "---\nname: 丁\ndescription: d\n---\n學習四，沒有任何來源標註")

	edges := DeriveEdges(root)
	if len(edges) != 1 {
		t.Fatalf("同任務的兩筆應產生 1 條邊，實際 %d：%+v", len(edges), edges)
	}
	e := edges[0]
	if e.From != "mem-aaa" || e.To != "mem-bbb" {
		t.Errorf("端點應是檔名 slug 且按字典序，實際 %s→%s", e.From, e.To)
	}
	if e.Type != "same-origin" || e.Source != "derived:provenance" {
		t.Errorf("邊的型別/來源不對：%+v", e)
	}
	if e.Confidence != 1.0 {
		t.Errorf("推導邊是確定性事實，信心應為 1.0，實際 %v", e.Confidence)
	}

	// 重跑要完全一致——否則既有的去重沒有意義。
	again := DeriveEdges(root)
	if len(again) != 1 || again[0] != e {
		t.Errorf("重跑結果不一致：%+v vs %+v", again, edges)
	}
}

// 單筆任務不該自連，沒有 provenance 的不該被猜關係。
func TestDeriveEdges_NoSpuriousLinks(t *testing.T) {
	root := t.TempDir()
	memDir := filepath.Join(root, ".claw", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMem(t, memDir, "mem-solo", "---\nname: 獨\ndescription: d\n---\n只有我\n\n（來源：任務「孤獨的任務」）")
	writeMem(t, memDir, "mem-none", "---\nname: 無\ndescription: d\n---\n沒有來源標註的記錄")

	if edges := DeriveEdges(root); len(edges) != 0 {
		t.Errorf("單筆任務與無 provenance 的記錄不該產生邊，實際 %+v", edges)
	}
}
