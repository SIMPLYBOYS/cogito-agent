package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func propFile(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claw", ProposedMemoryFileName),
		[]byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// 暫緩要有出口，但一定要有死線——「垃圾堆的本質是沒有到期日的暫存」。
func TestDefer_NeedsReason(t *testing.T) {
	root := propFile(t, "## [慣例] 來自任務「x」（t）\n- 甲\n")
	e := ListProposedMemory(root)[0]
	if err := DeferProposal(root, e, 3, "   "); err == nil {
		t.Fatal("沒寫原因就該擋下來——留在佇列裡的東西必須有人看得懂為什麼留")
	}
	if err := DeferProposal(root, e, 3, "等看完那份 spec"); err != nil {
		t.Fatal(err)
	}
	if note := DeferredNote(root, e); !strings.Contains(note, "等看完那份 spec") {
		t.Errorf("清單上要看得到原因與剩幾天，got %q", note)
	}
}

// 過期＝自動降為否決。這是死線唯一有意義的地方；不降級的話跟沒有死線一樣。
func TestDefer_ExpiresIntoReject(t *testing.T) {
	root := propFile(t, "## [慣例] 來自任務「x」（t）\n- 甲\n- 乙\n")
	all := ListProposedMemory(root)
	if err := DeferProposal(root, all[0], 1, "先擱著"); err != nil {
		t.Fatal(err)
	}
	// 把到期日改成過去（不能等一天）
	m := loadDeferred(root)
	for k, d := range m {
		d.Due = time.Now().Add(-time.Hour)
		m[k] = d
	}
	if err := saveDeferred(root, m); err != nil {
		t.Fatal(err)
	}

	gone := ExpireDeferred(root)
	if len(gone) != 1 || !strings.Contains(gone[0], "甲") {
		t.Fatalf("過期的該被降為否決，got %v", gone)
	}
	left := ListProposedMemory(root)
	if len(left) != 1 || !strings.Contains(left[0].Learning, "乙") {
		t.Fatalf("只該掉過期那條，其餘留著，got %+v", left)
	}
	if len(loadDeferred(root)) != 0 {
		t.Error("降級之後暫緩記錄要清掉，否則下次還會再處理一遍")
	}
}

// 鍵用【內容】不用編號：編號會隨著別條被放行/否決而位移，
// 拿它當鍵的話暫緩過的東西會在下一次操作後對到別條身上。
func TestDefer_KeyedByContentNotNumber(t *testing.T) {
	root := propFile(t, "## [慣例] 來自任務「x」（t）\n- 甲\n- 乙\n")
	all := ListProposedMemory(root)
	if err := DeferProposal(root, all[1], 3, "擱著"); err != nil { // 暫緩第 2 條「乙」
		t.Fatal(err)
	}
	if _, err := DiscardProposedMemory(root, 1); err != nil { // 否決第 1 條 → 乙 變成第 1 條
		t.Fatal(err)
	}
	now := ListProposedMemory(root)
	if len(now) != 1 || now[0].N != 1 {
		t.Fatalf("前置條件不成立：%+v", now)
	}
	if DeferredNote(root, now[0]) == "" {
		t.Error("編號位移之後對不回原來那條了——鍵不能用編號")
	}
}
