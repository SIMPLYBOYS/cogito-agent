package eval

import (
	"encoding/json"
	"testing"
)

// 鎖定 SuiteReport 的 JSON 形狀——這是 cmd/dashboard 解析報告的契約，欄位改名會破壞儀表板。
func TestSuiteReport_JSONContract(t *testing.T) {
	rep := SuiteReport{
		Model:        "claude-haiku-4-5",
		GeneratedAt:  "2026-06-23T10:00:00Z",
		Total:        2,
		Passed:       1,
		PassRate:     0.5,
		TotalCostUSD: 0.012,
		Results: []TestResult{
			{TestCaseID: "t1", Passed: true, TurnCount: 2, ToolErrorCount: 0, DurationMs: 1200, TotalCostUSD: 0.006},
			{TestCaseID: "t2", Passed: false, ErrorMsg: "boom", TurnCount: 5, ToolErrorCount: 3, DurationMs: 3400, TotalCostUSD: 0.006},
		},
	}

	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}

	// 關鍵欄位名必須穩定
	for _, key := range []string{
		`"model"`, `"generated_at"`, `"pass_rate"`, `"total_cost_usd"`,
		`"results"`, `"test_case_id"`, `"turn_count"`, `"tool_error_count"`, `"duration_ms"`,
	} {
		if !contains(string(data), key) {
			t.Errorf("報告 JSON 缺少欄位 %s", key)
		}
	}

	var back SuiteReport
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("round-trip 失敗: %v", err)
	}
	if back.PassRate != 0.5 || len(back.Results) != 2 || back.Results[1].ToolErrorCount != 3 {
		t.Errorf("round-trip 內容不符: %+v", back)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
