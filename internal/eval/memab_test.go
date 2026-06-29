package eval

import (
	"strings"
	"testing"
)

func TestMemoryABScenario_Valid(t *testing.T) {
	tc, mem := MemoryABScenario()
	if tc.SetupScript == "" || tc.TaskPrompt == "" || tc.ValidateScript == "" {
		t.Fatal("情境三段式不可空")
	}
	if !strings.Contains(mem, "conf/app/settings.ini") {
		t.Error("記憶記錄應點出設定檔位置（記憶的價值來源）")
	}
	if !strings.Contains(tc.ValidateScript, "version = 2.0") {
		t.Error("驗證腳本應檢查 version 改成 2.0")
	}
}

func TestMemoryABReport_RenderDelta(t *testing.T) {
	r := &MemoryABReport{
		Model: "m",
		Off:   ABRun{Label: "off", Passed: true, TurnCount: 8, TotalCostUSD: 0.03},
		On:    ABRun{Label: "on", Passed: true, TurnCount: 3, TotalCostUSD: 0.01},
	}
	out := r.Render()
	if !strings.Contains(out, "-5") { // 回合 delta = 3-8
		t.Errorf("應算出回合 delta -5：\n%s", out)
	}
}
