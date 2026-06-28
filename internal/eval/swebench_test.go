package eval

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSWEBench_JSONL(t *testing.T) {
	ins, err := LoadSWEBench(filepath.Join("testdata", "swebench_sample.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 2 {
		t.Fatalf("應載入 2 筆，got %d", len(ins))
	}
	if ins[0].Repo != "demo/repo" || ins[0].BaseCommit != "abc1234" {
		t.Errorf("欄位解析錯誤: %+v", ins[0])
	}
}

// FAIL_TO_PASS 兩種格式都要解得出：被字串化的陣列（第 1 筆）與原生陣列（第 2 筆）。
func TestTestList_TolerantFormats(t *testing.T) {
	ins, _ := LoadSWEBench(filepath.Join("testdata", "swebench_sample.jsonl"))
	if got := testList(ins[0].FailToPass); len(got) != 1 || got[0] != "test_add.py::test_add" {
		t.Errorf("字串化陣列解析錯誤: %v", got)
	}
	if got := testList(ins[1].FailToPass); len(got) != 1 || got[0] != "test_sub.py::test_sub" {
		t.Errorf("原生陣列解析錯誤: %v", got)
	}
}

func TestToTestCase_MapsAndDoesNotLeakSolution(t *testing.T) {
	ins, _ := LoadSWEBench(filepath.Join("testdata", "swebench_sample.jsonl"))
	tc := ins[0].ToTestCase(SWEOptions{})

	// Setup：clone + checkout 到 base_commit；不得含驗證測試或黃金解
	if !strings.Contains(tc.SetupScript, "git clone") || !strings.Contains(tc.SetupScript, "abc1234") {
		t.Errorf("Setup 應 clone 並 checkout base_commit:\n%s", tc.SetupScript)
	}
	if strings.Contains(tc.SetupScript, "test_add") {
		t.Error("Setup 不應含 test_patch（agent 解題時不該看到驗證測試）")
	}

	// Task：含 issue，不得洩漏黃金 patch / 測試
	if !strings.Contains(tc.TaskPrompt, "add(1,2) returns 4") {
		t.Error("Task 應含 problem_statement")
	}
	if strings.Contains(tc.TaskPrompt, "GOLD_PATCH") || strings.Contains(tc.TaskPrompt, "test_add") {
		t.Error("Task 不應洩漏黃金解或驗證測試（防作弊）")
	}

	// Validate：先套 test_patch 再跑 FAIL_TO_PASS 的測試
	if !strings.Contains(tc.ValidateScript, "git apply") || !strings.Contains(tc.ValidateScript, "test_add.py::test_add") {
		t.Errorf("Validate 應套 test_patch 並跑指定測試:\n%s", tc.ValidateScript)
	}
	if !strings.Contains(tc.ValidateScript, "pytest") {
		t.Error("Validate 預設應用 pytest 跑測試")
	}
}

func TestSWEToTestCases_Limit(t *testing.T) {
	ins, _ := LoadSWEBench(filepath.Join("testdata", "swebench_sample.jsonl"))
	if got := SWEToTestCases(ins, SWEOptions{}, 1); len(got) != 1 {
		t.Errorf("limit=1 應只轉 1 筆，got %d", len(got))
	}
	if got := SWEToTestCases(ins, SWEOptions{}, 0); len(got) != 2 {
		t.Errorf("limit=0 應全轉，got %d", len(got))
	}
}
