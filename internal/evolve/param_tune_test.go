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
