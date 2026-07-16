package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInWorkDir(t *testing.T) {
	wd := "/work/space"
	// 允許：工作區內的相對路徑（含內層 ..，只要沒逃出）。回傳路徑必須仍在 wd 底下。
	// 絕對路徑輸入被 filepath.Join 收編進 wd（不逃逸），故也算允許但被限制在區內。
	for _, ok := range []string{"a.go", "src/main.go", "src/../a.go", "./x", "/etc/passwd"} {
		full, err := resolveInWorkDir(wd, ok)
		if err != nil {
			t.Errorf("%q 應允許，卻被拒: %v", ok, err)
			continue
		}
		if !strings.HasPrefix(full, wd) {
			t.Errorf("%q 解析結果 %q 逃出了工作區", ok, full)
		}
	}
	// 拒絕：../ 穿越到工作區之外
	for _, bad := range []string{"../../etc/passwd", "../secret", "src/../../x"} {
		if _, err := resolveInWorkDir(wd, bad); err == nil {
			t.Errorf("%q 應被拒（逃出工作區），卻通過", bad)
		}
	}
}

// symlink 逃逸：agent 的 bash 可以在工作區內種 symlink 指向區外（COGITO_SANDBOX=docker 時 bash 被關在
// 容器裡，但檔案工具一律跑在【宿主機】——symlink 的 target 在誰的 namespace 解就在誰的 namespace 生效，
// 於是容器內種的 symlink 讓宿主機的 write_file 寫進宿主機任意路徑，硬隔離形同虛設）。
// 只比對字串前綴擋不住這條：路徑裡沒有任何 ".."。
func TestResolveInWorkDir_RejectsSymlinkEscape(t *testing.T) {
	outside := t.TempDir() // 模擬工作區【外】的宿主機檔案系統
	wd := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("HOST-ONLY"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 三種種法：指向區外檔案 / 指向區外目錄 / 中間路徑段是 symlink
	if err := os.Symlink(secret, filepath.Join(wd, "flink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(wd, "dlink")); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"flink", "dlink/secret.txt", "dlink/newfile.txt"} {
		full, err := resolveInWorkDir(wd, bad)
		if err == nil {
			t.Errorf("%q 應被拒（經 symlink 逃出工作區），卻通過並解析成 %q", bad, full)
		}
	}
}

// 修 symlink 逃逸不能誤傷正常用法：write_file 會建新檔與新目錄（此時路徑尚不存在，
// 對不存在的路徑直接 EvalSymlinks 會失敗），workDir 本身也可能是 symlink（macOS 的
// /var → /private/var、t.TempDir() 即是）。這條守著「別為了修洞把工具弄殘」。
func TestResolveInWorkDir_AllowsLegitimatePaths(t *testing.T) {
	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "src", "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 區內的 symlink（指向區內）應照常可用——這是合法的
	if err := os.Symlink(filepath.Join(wd, "src", "main.go"), filepath.Join(wd, "inner")); err != nil {
		t.Fatal(err)
	}

	root, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{
		"src/main.go",       // 已存在的檔
		"new.txt",           // 新檔（尚不存在）
		"deep/nested/x.txt", // 新檔 + 新目錄（父層也不存在）
		"inner",             // 區內 symlink
		"src/../src/main.go",
	} {
		full, err := resolveInWorkDir(wd, ok)
		if err != nil {
			t.Errorf("%q 是正常用法，不該被拒: %v", ok, err)
			continue
		}
		if !strings.HasPrefix(full, root) {
			t.Errorf("%q 解析成 %q，不在工作區 %q 底下", ok, full, root)
		}
	}
}
