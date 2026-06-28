package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// SWEInstance 是一筆 SWE-bench 實例（對齊 princeton-nlp/SWE-bench 的資料欄位）。
// FAIL_TO_PASS / PASS_TO_PASS 在官方資料常是「被字串化的 JSON 陣列」，故用 RawMessage 容兩種形式。
type SWEInstance struct {
	InstanceID       string          `json:"instance_id"`
	Repo             string          `json:"repo"`              // "owner/name"
	BaseCommit       string          `json:"base_commit"`       // 解題的起點提交
	ProblemStatement string          `json:"problem_statement"` // GitHub issue → 任務指令
	Patch            string          `json:"patch"`             // 黃金解：評測【不給】agent，僅供人對照
	TestPatch        string          `json:"test_patch"`        // 驗證用測試：agent 解題時看不到，驗證階段才套上（防作弊）
	FailToPass       json.RawMessage `json:"FAIL_TO_PASS"`      // 修好後應由 fail→pass 的測試
	PassToPass       json.RawMessage `json:"PASS_TO_PASS"`      // 修好後應維持 pass 的測試（防改壞）
	Version          string          `json:"version"`
}

// LoadSWEBench 讀 SWE-bench 資料檔：每行一個 JSON 物件（JSONL），或整檔一個 JSON 陣列。
func LoadSWEBench(path string) ([]SWEInstance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("讀 SWE-bench 檔失敗: %w", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") { // 整檔 JSON 陣列
		var arr []SWEInstance
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, fmt.Errorf("解析 JSON 陣列失敗: %w", err)
		}
		return arr, nil
	}
	var out []SWEInstance // JSONL：逐行
	sc := bufio.NewScanner(strings.NewReader(trimmed))
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20) // 容大行：patch 可能很長
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ins SWEInstance
		if err := json.Unmarshal([]byte(line), &ins); err != nil {
			return nil, fmt.Errorf("解析 JSONL 行失敗: %w", err)
		}
		out = append(out, ins)
	}
	return out, sc.Err()
}

// testList 容忍兩種格式：直接的 JSON 陣列，或被字串化的 JSON 陣列（官方資料常見後者）。
func testList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		_ = json.Unmarshal([]byte(s), &arr)
	}
	return arr
}

// SWEOptions 控制轉換出的腳本中「環境安裝」與「測試命令」——SWE-bench 各 repo 的 Python 環境差異大，
// 正式跑通常在官方 Docker 映像內（依賴已備）；此處留可覆蓋的鉤子，預設不裝（交給執行環境）。
type SWEOptions struct {
	RepoURLPrefix string // 預設 https://github.com/
	EnvSetup      string // 安裝該 repo 環境的 bash（如 "pip install -e . -q"）；空＝跳過
	TestRunner    string // 跑測試的命令前綴，預設 "python -m pytest -q"
}

func (o SWEOptions) withDefaults() SWEOptions {
	if o.RepoURLPrefix == "" {
		o.RepoURLPrefix = "https://github.com/"
	}
	if o.TestRunner == "" {
		o.TestRunner = "python -m pytest -q"
	}
	return o
}

// ToTestCase 把一個 SWE-bench 實例轉成評測框架的三段式 TestCase：
//   - Setup：clone 該 repo 並 checkout 到 base_commit（【不含】test_patch——agent 解題時看不到驗證測試）+ 環境安裝
//   - Task：problem_statement（GitHub issue），要 agent 只改原始碼把問題修好
//   - Validate：套上 test_patch（加入驗證測試）後跑 FAIL_TO_PASS（+PASS_TO_PASS），全過 exit 0 即通過
//
// 黃金 patch / test_patch 都【不】進 Task，agent 無從照抄；test_patch 在 agent 跑完後才套，無法被改掉。
func (ins SWEInstance) ToTestCase(opts SWEOptions) TestCase {
	o := opts.withDefaults()
	url := o.RepoURLPrefix + ins.Repo + ".git"

	setup := fmt.Sprintf("set -e\ngit clone --quiet %s .\ngit checkout --quiet %s\n", url, ins.BaseCommit)
	if o.EnvSetup != "" {
		setup += o.EnvSetup + "\n"
	}

	tests := append(testList(ins.FailToPass), testList(ins.PassToPass)...)
	const patchFile = "/tmp/swe_testpatch.diff"
	validate := fmt.Sprintf("set -e\n%s\ngit apply %s\n%s %s\n",
		writeFileHeredoc(patchFile, ins.TestPatch), patchFile,
		o.TestRunner, strings.Join(shellQuoteAll(tests), " "))

	task := fmt.Sprintf(`你正位於一個已 clone 的 Git 倉庫工作區（%s，已 checkout 到問題發生的提交）。請閱讀下面的 GitHub issue，直接修改【原始碼】使問題解決。
規則：只動原始碼，不要新增或修改測試檔、不要試圖執行測試。改完即可。

# Issue
%s`, ins.Repo, ins.ProblemStatement)

	return TestCase{
		ID:             "swe_" + sanitizeID(ins.InstanceID),
		Name:           "SWE-bench: " + ins.InstanceID,
		SetupScript:    setup,
		TaskPrompt:     task,
		ValidateScript: validate,
	}
}

// SWEToTestCases 批次轉換；limit>0 時只取前 N 個。
func SWEToTestCases(instances []SWEInstance, opts SWEOptions, limit int) []TestCase {
	if limit > 0 && limit < len(instances) {
		instances = instances[:limit]
	}
	out := make([]TestCase, 0, len(instances))
	for _, ins := range instances {
		out = append(out, ins.ToTestCase(opts))
	}
	return out
}

// writeFileHeredoc 產生「把內容寫進檔案」的 bash 片段；用單引號 here-doc 避免 shell 展開，patch 照字面寫出。
// ponytail: 假設 patch 內容不含結束標記行（極罕見）；若要絕對安全，改用 base64 編碼夾帶。
func writeFileHeredoc(path, content string) string {
	return fmt.Sprintf("cat > %s <<'SWE_EOF_MARKER'\n%s\nSWE_EOF_MARKER", path, content)
}

func shellQuoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "'" + strings.ReplaceAll(x, "'", `'\''`) + "'"
	}
	return out
}

func sanitizeID(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == ':' || r == '\\' {
			return '_'
		}
		return r
	}, s)
}
