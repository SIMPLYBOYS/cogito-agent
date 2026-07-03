package tools

import (
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
