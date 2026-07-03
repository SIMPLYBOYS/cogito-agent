package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveInWorkDir 把使用者/模型提供的相對路徑解析成 workDir 內的絕對路徑，並硬性擋掉逃逸
// （絕對路徑、../ 穿越）。檔案工具必須走這裡而非直接 filepath.Join，因為 Join 會把 "../../etc/passwd"
// clean 成宿主機真實路徑——這層是工具層的硬邊界，不依賴可被繞過的審批。
// ponytail: 只比對 clean 後的相對路徑前綴，不解 symlink；workDir 是 agent 專屬 scratch，
// 若日後允許不受信來源建 symlink，升級路徑是對 full 做 filepath.EvalSymlinks 後重驗前綴。
func resolveInWorkDir(workDir, path string) (string, error) {
	full := filepath.Join(workDir, path)
	rel, err := filepath.Rel(workDir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("路徑逃出工作區，已拒絕: %s", path)
	}
	return full, nil
}
