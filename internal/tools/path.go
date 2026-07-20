package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// evalExisting 對 p 最深的【已存在】祖先解 symlink，再把尚未存在的剩餘片段接回去。
// 為什麼要這一圈：write_file / edit_file 會建立新檔（甚至新的父目錄），對不存在的路徑直接
// EvalSymlinks 一定失敗；但又不能因此跳過解析——否則「symlink 指向的新檔」就繞過了檢查。
// 折衷是只解「存在的那一段」（symlink 只可能存在於已存在的部分），剩下的純字串接回。
func evalExisting(p string) (string, error) {
	rest := ""
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err // ELOOP / 權限不足等 → 一律當失敗（fail-closed）
		}
		parent := filepath.Dir(p)
		if parent == p { // 一路走到根都不存在（理論上不會，根一定在）
			return "", fmt.Errorf("無法解析路徑: %s", p)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// resolveInWorkDir 把使用者/模型提供的相對路徑解析成 workDir 內的絕對路徑，並硬性擋掉逃逸
// （絕對路徑、../ 穿越、symlink）。檔案工具必須走這裡而非直接 filepath.Join，因為 Join 會把
// "../../etc/passwd" clean 成宿主機真實路徑——這層是工具層的硬邊界，不依賴可被繞過的審批。
//
// symlink 必須解：agent 的 bash 可以在工作區內種一個指向區外的 symlink，而路徑字串裡沒有任何
// ".."，純前綴比對完全看不到。這在 COGITO_SANDBOX=docker 下尤其致命——bash 被關在容器裡，但
// 檔案工具一律跑在【宿主機】，symlink 的 target 在誰的 namespace 解就在誰的 namespace 生效，
// 於是「容器內種 symlink → 宿主機的 write_file 跟著寫出去」讓硬隔離形同虛設。
// workDir 自己也要解：macOS 的 /var → /private/var、/tmp → /private/tmp，不解則正常路徑會被誤判逃逸。
//
// ponytail: 解析與實際開檔之間仍有 TOCTOU 窗口（bash 可在這之間把某段換成 symlink）。要徹底關掉
// 得用 openat2(RESOLVE_BENEATH)（Linux）或逐段 O_NOFOLLOW 開檔，屆時再說；現況已擋掉「先種後用」
// 這個真正好用的攻擊路徑。
// controlDir 是 agent 的【控制面】：技能、記憶、調參、排程都住這。
//
// 【為何寫入要擋】這裡的檔案決定 agent 未來的行為，等於它自己的規則書：
//   - .claw/skills/      生效技能——本該只能經 governance 的把關流程晉升（見 evolve.Promote）
//   - .claw/config.json  執行期護欄（MaxTurns / MaxCostUSD）——能改就等於能解除自己的上限
//   - .claw/memory/      長期記憶——提案本該人工放行
//   - .claw/cron.json    排程任務——【能寫就等於能自己排程】，Hermes 也專門防這條
//
// 少了這層，「產物只進暫存區、需人工放行」的整條 HITL 鏈可以被一句 write_file 繞過。
const controlDir = ".claw"

// resolveForWrite 同 resolveInWorkDir，另外擋掉寫入控制面（.claw/）。
//
// 邊界說明：這只管檔案工具。bash 仍能 `echo > .claw/config.json`——那條靠危險命令審批與
// Docker 隔離，不在本層。擋住檔案工具已經關掉最順手的那條路（模型偏好用結構化工具改檔）。
func resolveForWrite(workDir, path string) (string, error) {
	full, err := resolveInWorkDir(workDir, path)
	if err != nil {
		return "", err
	}
	root, err := evalExisting(workDir)
	if err != nil {
		return "", fmt.Errorf("工作區路徑解析失敗: %w", err)
	}
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("路徑解析失敗: %w", err)
	}
	if rel == controlDir || strings.HasPrefix(rel, controlDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("拒絕寫入 agent 控制面 %s/（技能／記憶／調參／排程須經治理流程放行）: %s",
			controlDir, path)
	}
	return full, nil
}

func resolveInWorkDir(workDir, path string) (string, error) {
	root, err := evalExisting(workDir)
	if err != nil {
		return "", fmt.Errorf("工作區路徑解析失敗: %w", err)
	}
	full, err := evalExisting(filepath.Join(root, path))
	if err != nil {
		return "", fmt.Errorf("路徑解析失敗: %w", err)
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("路徑逃出工作區，已拒絕: %s", path)
	}
	return full, nil
}
