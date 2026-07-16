package evolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var defaultKnobs = Knobs{MaxTurns: 40, MaxConcurrentTools: 5, MaxCostUSD: 1.0}

func findProposal(ps []Proposal, knob string) *Proposal {
	for i := range ps {
		if ps[i].Knob == knob {
			return &ps[i]
		}
	}
	return nil
}

func TestAdvise_HighToolErrors_LowersConcurrency(t *testing.T) {
	ps := Advise(RunStats{N: 5, PassRate: 0.8, MeanTurns: 8, MeanToolErrors: 4}, defaultKnobs)
	p := findProposal(ps, "max_concurrent_tools")
	if p == nil {
		t.Fatal("工具報錯高時應提議降併發")
	}
	if p.Proposed.(int) != 4 || p.Sensitive {
		t.Errorf("應降到 4 且非敏感（收緊方向），got %+v", p)
	}
}

func TestAdvise_EfficientHighPass_TightensTurns(t *testing.T) {
	ps := Advise(RunStats{N: 10, PassRate: 1.0, MeanTurns: 5, MeanToolErrors: 0}, defaultKnobs)
	p := findProposal(ps, "max_turns")
	if p == nil {
		t.Fatal("高通過+低回合時應提議收緊回合上限")
	}
	if p.Proposed.(int) >= 40 || p.Sensitive {
		t.Errorf("收緊應 <40 且非敏感，got %+v", p)
	}
}

func TestAdvise_CeilingHits_RaisesTurns_Sensitive(t *testing.T) {
	ps := Advise(RunStats{N: 10, PassRate: 0.4, MeanTurns: 38, MeanToolErrors: 1, CeilingHitRate: 0.5}, defaultKnobs)
	p := findProposal(ps, "max_turns")
	if p == nil || !p.Sensitive {
		t.Fatalf("觸頂多時應提議放寬回合上限且標 Sensitive，got %+v", p)
	}
	if p.Proposed.(int) <= 40 || p.Proposed.(int) > maxTurns {
		t.Errorf("放寬應 >40 且 <=上限 %d，got %v", maxTurns, p.Proposed)
	}
}

func TestAdvise_CostNearCeiling_RaisesCost_Sensitive(t *testing.T) {
	ps := Advise(RunStats{N: 3, PassRate: 1.0, MeanTurns: 10, MaxCaseCostUSD: 0.95}, defaultKnobs)
	p := findProposal(ps, "max_cost_usd")
	if p == nil || !p.Sensitive {
		t.Fatalf("逼近成本上限時應提議放寬且標 Sensitive，got %+v", p)
	}
}

func TestAdvise_Bounds_NeverExceed(t *testing.T) {
	// 已在上限附近，放寬提案不得超界
	cur := Knobs{MaxTurns: 58, MaxConcurrentTools: 1, MaxCostUSD: 4.8}
	ps := Advise(RunStats{N: 10, PassRate: 0.2, MeanTurns: 57, CeilingHitRate: 0.9, MaxCaseCostUSD: 4.7}, cur)
	if p := findProposal(ps, "max_turns"); p != nil && p.Proposed.(int) > maxTurns {
		t.Errorf("max_turns 提案超界: %v", p.Proposed)
	}
	if p := findProposal(ps, "max_cost_usd"); p != nil && p.Proposed.(float64) > maxCostUSD {
		t.Errorf("max_cost_usd 提案超界: %v", p.Proposed)
	}
}

func TestAdvise_Healthy_NoProposals(t *testing.T) {
	ps := Advise(RunStats{N: 10, PassRate: 0.8, MeanTurns: 20, MeanToolErrors: 1, MaxCaseCostUSD: 0.3}, defaultKnobs)
	if len(ps) != 0 {
		t.Errorf("健康指標不該有提案，got %+v", ps)
	}
}

func TestWriteProposedConfig(t *testing.T) {
	dir := t.TempDir()
	ps := []Proposal{{Knob: "max_concurrent_tools", Current: 5, Proposed: 4, Reason: "r"}}
	path, err := WriteProposedConfig(dir, defaultKnobs, ps)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if filepath.Base(path) != ProposedConfigFileName {
		t.Errorf("檔名應為 %s", ProposedConfigFileName)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("提案檔應為合法 JSON: %v", err)
	}
	if !strings.Contains(string(data), "不會自動生效") {
		t.Error("提案檔應含安全提示")
	}
}

// C1 閉環：提案 → apply 晉升為 config.json → LoadKnobs 讀得到；套用時再 clamp（防手改提案繞過上限）。
func TestApplyProposedConfig_PromotesAndClamps(t *testing.T) {
	root := t.TempDir()
	clawDir := filepath.Join(root, ".claw")

	// 手工造一份提案：一個合法收緊(max_turns 40→20) + 一個【超界】值(max_cost_usd 999，應被 clamp 到 5.0)。
	proposals := []Proposal{
		{Knob: "max_turns", Current: 40, Proposed: 20, Reason: "收緊"},
		{Knob: "max_cost_usd", Current: 1.0, Proposed: 999.0, Reason: "越界試探", Sensitive: true},
	}
	if _, err := WriteProposedConfig(clawDir, defaultKnobs, proposals); err != nil {
		t.Fatal(err)
	}

	changes, err := ApplyProposedConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("應有 2 條變更，got %v", changes)
	}

	// 提案檔應已清除
	if _, err := os.Stat(filepath.Join(clawDir, ProposedConfigFileName)); !os.IsNotExist(err) {
		t.Error("套用後提案檔應被刪除")
	}
	// LoadKnobs 讀得到，且越界值被 clamp
	k, ok := LoadKnobs(root)
	if !ok {
		t.Fatal("套用後 LoadKnobs 應讀得到 config.json")
	}
	if k.MaxTurns != 20 {
		t.Errorf("max_turns 應套用為 20，got %d", k.MaxTurns)
	}
	if k.MaxCostUSD != maxCostUSD { // 999 被 clamp 到上界 5.0
		t.Errorf("越界的 max_cost_usd 應被 clamp 到 %.1f，got %.2f", maxCostUSD, k.MaxCostUSD)
	}
	if k.MaxConcurrentTools != defaultKnobs.MaxConcurrentTools { // 未提案的沿用基底
		t.Errorf("未提案的 max_concurrent_tools 應維持基底 %d，got %d", defaultKnobs.MaxConcurrentTools, k.MaxConcurrentTools)
	}
}

func TestLoadKnobs_MissingReturnsFalse(t *testing.T) {
	if _, ok := LoadKnobs(t.TempDir()); ok {
		t.Error("無 config.json 應回 false（呼叫端沿用引擎預設）")
	}
}

func TestApplyProposedConfig_NoProposalNoop(t *testing.T) {
	changes, err := ApplyProposedConfig(t.TempDir())
	if err != nil || changes != nil {
		t.Errorf("無提案應回 (nil,nil)，got changes=%v err=%v", changes, err)
	}
}

// 提案檔的【基底】(doc.Current) 同樣來自磁碟、同樣不可信：原本只 clamp 各提案值，
// 於是塞一個超界基底 + 任一合法提案，超界值就原封不動落進 config.json——成本熔斷實質關閉。
// （能寫提案檔的人：host 模式下的 bash 本來就能；docker 模式下靠已修掉的 symlink 逃逸。
// 而 `apply config` 只過 allowlist、不過 admin 閘，任一授權使用者都能觸發套用。）
func TestApplyProposedConfig_ClampsMaliciousBase(t *testing.T) {
	root := t.TempDir()
	clawDir := filepath.Join(root, ".claw")

	evil := Knobs{MaxTurns: 9999, MaxConcurrentTools: 999, MaxCostUSD: 99999}
	// 只帶一個無關且合法的提案，把 evil 基底夾帶進去
	proposals := []Proposal{{Knob: "max_concurrent_tools", Current: 4, Proposed: 4, Reason: "夾帶"}}
	if _, err := WriteProposedConfig(clawDir, evil, proposals); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyProposedConfig(root); err != nil {
		t.Fatal(err)
	}

	k, ok := LoadKnobs(root)
	if !ok {
		t.Fatal("應已寫出 config.json")
	}
	if k.MaxCostUSD > maxCostUSD {
		t.Errorf("惡意基底的 max_cost_usd 未被 clamp: %.0f（上限 %.1f）——成本熔斷會被關掉", k.MaxCostUSD, maxCostUSD)
	}
	if k.MaxTurns > maxTurns {
		t.Errorf("惡意基底的 max_turns 未被 clamp: %d（上限 %d）", k.MaxTurns, maxTurns)
	}
	if k.MaxConcurrentTools > maxConcurrency {
		t.Errorf("惡意基底的 max_concurrent_tools 未被 clamp: %d（上限 %d）", k.MaxConcurrentTools, maxConcurrency)
	}
}

// 0 的語意是「不覆蓋，用引擎預設」（cmd/claw/main.go 是 `if k.X > 0` 才套用）。
// clamp 基底時若無條件套用，0 會被 clamp 成下限，把「不覆蓋」悄悄變成「max_turns=10 / $0.5」——
// 反而收緊了限制。這條守著那個語意。
func TestApplyProposedConfig_ZeroMeansUnset(t *testing.T) {
	root := t.TempDir()
	clawDir := filepath.Join(root, ".claw")

	// 基底只設 max_turns，其餘留 0（＝不覆蓋）
	base := Knobs{MaxTurns: 40}
	proposals := []Proposal{{Knob: "max_turns", Current: 40, Proposed: 30, Reason: "收緊"}}
	if _, err := WriteProposedConfig(clawDir, base, proposals); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyProposedConfig(root); err != nil {
		t.Fatal(err)
	}

	k, _ := LoadKnobs(root)
	if k.MaxCostUSD != 0 {
		t.Errorf("未設的 max_cost_usd 應維持 0（不覆蓋），卻被 clamp 成 %.2f", k.MaxCostUSD)
	}
	if k.MaxConcurrentTools != 0 {
		t.Errorf("未設的 max_concurrent_tools 應維持 0（不覆蓋），卻被 clamp 成 %d", k.MaxConcurrentTools)
	}
	if k.MaxTurns != 30 {
		t.Errorf("有設的 max_turns 應為提案值 30，got %d", k.MaxTurns)
	}
}
