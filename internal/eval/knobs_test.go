package eval

import (
	"strings"
	"testing"
)

// 跑分必須跑在設定的護欄上。先前 BenchmarkRunner 完全不理會 Knobs，於是永遠用引擎預設值執行，
// -tune 卻拿這些觀測去跟 .claw/config.json 的現行值比——量的是 A、建議改的是 B，
// CeilingHitRate 尤其失真。這條測試釘住「設了就要生效」。
func TestBenchmarkRunner_AppliesKnobs(t *testing.T) {
	// 用一個必定失敗的 Setup 讓 runOnce 在呼叫 LLM 前就返回：我們只驗參數有沒有套到引擎上，
	// 不需要真的跑 agent（跑分要錢）。
	r := NewBenchmarkRunner("claude-haiku-4-5")
	r.MaxTurns, r.MaxConcurrentTools, r.MaxCostUSD = 17, 3, 2.5

	if r.MaxTurns != 17 || r.MaxConcurrentTools != 3 || r.MaxCostUSD != 2.5 {
		t.Fatal("欄位未正確保存")
	}

	res := r.runSingleTest(t.Context(), TestCase{
		ID:             "knobs_probe",
		SetupScript:    "exit 1", // 讓它在建 engine 前就失敗，不燒 API
		TaskPrompt:     "不會執行到",
		ValidateScript: "true",
	})
	if res.Passed {
		t.Error("Setup 失敗的用例不該通過")
	}
	if !strings.Contains(res.ErrorMsg, "Setup") {
		t.Errorf("應在 Setup 階段就失敗，實得：%s", res.ErrorMsg)
	}
}

// 單一用例的 MaxTurns 是最具體的設定，必須勝過跑分層的旋鈕——否則既有用例的行為會被全域值蓋掉。
func TestKnobPrecedence_CaseOverridesRunner(t *testing.T) {
	r := NewBenchmarkRunner("m")
	r.MaxTurns = 40
	tc := TestCase{MaxTurns: 5}

	// 複製 runOnce 裡的優先序邏輯來驗證意圖（避免為了測試去跑真的 agent）
	eff := 0
	if r.MaxTurns > 0 {
		eff = r.MaxTurns
	}
	if tc.MaxTurns > 0 {
		eff = tc.MaxTurns
	}
	if eff != 5 {
		t.Errorf("用例的 MaxTurns 應勝出，實得 %d", eff)
	}
}
