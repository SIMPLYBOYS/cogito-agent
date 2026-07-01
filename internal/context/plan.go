package context

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// TodoProgress 是框架對 TODO.md checkbox 帳本的【確定性】解讀——斷點續跑時「哪些做完、下一步是哪個」
// 的權威來源。有了它，續跑跳過已完成步驟就不再靠 LLM 重讀 TODO.md 自己猜（可能重做或漏做），
// 而是由框架算出、注入上下文錨定。
type TodoProgress struct {
	Total    int
	Done     int
	NextStep string // 第一個未打勾步驟的文字（去 "- [ ]"）；全部完成時為空
}

// ReadTodoProgress 解析 workDir/TODO.md 的 markdown checkbox。bool 表示 TODO.md 存在且至少含一個 checkbox
// （否則視為「沒有帳本」，呼叫端不注入進度錨）。只讀不寫、零依賴、確定性。
func ReadTodoProgress(workDir string) (TodoProgress, bool) {
	f, err := os.Open(filepath.Join(workDir, "TODO.md"))
	if err != nil {
		return TodoProgress{}, false
	}
	defer f.Close()

	var p TodoProgress
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		done, text, ok := parseCheckbox(strings.TrimSpace(sc.Text()))
		if !ok {
			continue
		}
		p.Total++
		if done {
			p.Done++
		} else if p.NextStep == "" {
			p.NextStep = text
		}
	}
	if p.Total == 0 {
		return TodoProgress{}, false
	}
	return p, true
}

// parseCheckbox 認 "- [ ] 文字" / "- [x] 文字"（x 不分大小寫，允許 - 或 * 作項目符號）。
func parseCheckbox(line string) (done bool, text string, ok bool) {
	if len(line) < 4 || (line[0] != '-' && line[0] != '*') {
		return false, "", false
	}
	rest := strings.TrimSpace(line[1:]) // 去項目符號 → 應為 "[ ] ..." / "[x] ..."
	if len(rest) < 3 || rest[0] != '[' || rest[2] != ']' {
		return false, "", false
	}
	switch rest[1] {
	case ' ':
		done = false
	case 'x', 'X':
		done = true
	default:
		return false, "", false
	}
	return done, strings.TrimSpace(rest[3:]), true
}
