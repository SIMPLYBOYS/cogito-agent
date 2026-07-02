package eval

import (
	"strings"
	"testing"
)

// 技能 A/B 情境的自檢（不呼叫 LLM）：情境三段式齊全、技能正文含隱藏慣例、Render 出對照 + delta。
func TestSkillABScenario_WellFormed(t *testing.T) {
	tc, doc, name := SkillABScenario()

	if tc.SetupScript == "" || tc.TaskPrompt == "" || tc.ValidateScript == "" {
		t.Fatal("情境三段式（Setup/Task/Validate）不可為空")
	}
	if name == "" || !strings.Contains(doc, "name: "+name) {
		t.Errorf("技能正文 frontmatter 應含 name: %s", name)
	}
	// 驗證腳本要求「兩個檔都對」——這正是無技能會漏掉的隱藏慣例。
	if !strings.Contains(tc.ValidateScript, "VERSION") || !strings.Contains(tc.ValidateScript, "package.json") {
		t.Errorf("驗證應同時檢查 VERSION 與 package.json（隱藏雙檔慣例）：%s", tc.ValidateScript)
	}
}

func TestSkillABReport_Render(t *testing.T) {
	rep := &SkillABReport{
		Model: "m",
		Off:   ABRun{Label: "off", Passed: false, TurnCount: 8, TotalCostUSD: 0.03},
		On:    ABRun{Label: "on", Passed: true, TurnCount: 3, TotalCostUSD: 0.01},
	}
	out := rep.Render()
	if !strings.Contains(out, "技能任務影響 A/B") || !strings.Contains(out, "Δ(on−off)") {
		t.Errorf("Render 應含標題與 delta：\n%s", out)
	}
	if !strings.Contains(out, "-5") { // 回合 3-8 = -5
		t.Errorf("delta 應顯示回合 -5：\n%s", out)
	}
}
