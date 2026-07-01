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

// planGoalRuneCap 是目標錨每輪注入的字數上限：取 PLAN.md 開頭（＝目標/理解），避免整份架構每輪灌爆。
const planGoalRuneCap = 800

// ReadPlanGoal 讀 workDir/PLAN.md 的開頭當「原始目標」錨（去掉單行 markdown 標題、rune 安全截斷）。
// bool 表示 PLAN.md 存在且有實質內容。讓框架每輪把原始目標釘進 system 訊息，對抗多輪目標漂移
// （Context Drift）——一切步驟都必須服務這個目標。
func ReadPlanGoal(workDir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(workDir, "PLAN.md"))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(b))
	// 去掉開頭單一 markdown 標題行（如 "# 計畫"），留實質目標內容。
	if strings.HasPrefix(s, "#") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		} else {
			s = ""
		}
	}
	if s == "" {
		return "", false
	}
	if r := []rune(s); len(r) > planGoalRuneCap {
		s = string(r[:planGoalRuneCap]) + " …[目標錨截斷]…"
	}
	return s, true
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
