package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 造一個小 repo：src.go 的第 4 行是 func Alpha、第 8 行是 func Beta。
func citeFixture(t *testing.T) string {
	t.Helper()
	wd := t.TempDir()
	src := "package x\n\nimport \"fmt\"\n\nfunc Alpha() {\n\tfmt.Println(1)\n}\n\nfunc Beta() {\n\tfmt.Println(2)\n}\n"
	if err := os.WriteFile(filepath.Join(wd, "src.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return wd
}

func runVerify(t *testing.T, wd, doc string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wd, "wiki.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := NewVerifyCitationsTool(wd).Execute(context.Background(), json.RawMessage(`{"path":"wiki.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVerifyCitations_AllGood(t *testing.T) {
	wd := citeFixture(t)
	// Alpha 實際在第 5 行；引用寫 5（準）與 4（差一行，在 ±2 容差內——引用常指向區塊開頭）
	out := runVerify(t, wd, "說明〔`src.go:5` · `func Alpha`〕與〔src.go:4 · func Alpha〕。\n"+
		"區間引用也要能過：〔`src.go:9-11` · `func Beta`〕\n")
	if !strings.HasPrefix(out, "✓") {
		t.Fatalf("正確的引用不該被判錯:\n%s", out)
	}
}

// 核心價值：行號錯了要報出【實際在第幾行】——不只判錯，還要能一輪改對。
func TestVerifyCitations_WrongLineReportsActual(t *testing.T) {
	wd := citeFixture(t)
	out := runVerify(t, wd, "〔`src.go:42` · `func Beta`〕\n")
	if !strings.Contains(out, "實際在第 9 行") {
		t.Fatalf("應指出 func Beta 實際在第 9 行:\n%s", out)
	}
}

// 錨點根本不存在＝模型掰了一個符號出來。這種不能只說「行號錯」，要說「這東西不存在」。
func TestVerifyCitations_MissingAnchor(t *testing.T) {
	wd := citeFixture(t)
	out := runVerify(t, wd, "〔`src.go:5` · `func Gamma`〕\n")
	if !strings.Contains(out, "全檔找不到這個錨點") {
		t.Fatalf("不存在的符號應被指名:\n%s", out)
	}
}

func TestVerifyCitations_MissingFile(t *testing.T) {
	wd := citeFixture(t)
	out := runVerify(t, wd, "〔`nope.go:1` · `func Alpha`〕\n")
	if !strings.Contains(out, "檔案不存在") {
		t.Fatalf("不存在的檔案應被指名:\n%s", out)
	}
}

// 一份「描述了程式碼卻沒有任何引用」的文件不算通過——那正是 DeepWiki 式的空話。
func TestVerifyCitations_NoCitationsIsNotPass(t *testing.T) {
	wd := citeFixture(t)
	out := runVerify(t, wd, "這個模組管理狀態機，很重要。\n")
	if strings.HasPrefix(out, "✓") || !strings.Contains(out, "沒有任何") {
		t.Fatalf("無引用不該回報通過:\n%s", out)
	}
}

// 路徑護欄：引用不得指出工作區（文件本身的路徑也一樣，由 ResolveInWorkDir 擋）。
func TestVerifyCitations_PathEscape(t *testing.T) {
	wd := citeFixture(t)
	out := runVerify(t, wd, "〔`../../etc/passwd:1` · `root`〕\n")
	if !strings.Contains(out, "不在工作區內") && !strings.Contains(out, "檔案不存在") {
		t.Fatalf("越界路徑應被擋:\n%s", out)
	}
	if _, err := NewVerifyCitationsTool(wd).Execute(context.Background(),
		json.RawMessage(`{"path":"../../../etc/passwd"}`)); err == nil {
		t.Error("文件本身的路徑越界應直接回錯")
	}
}
