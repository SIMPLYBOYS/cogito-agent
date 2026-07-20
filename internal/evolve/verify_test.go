package evolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 只有「可能造成回歸」的方向才值得再花一次 API 費用重跑。
// 放寬上限不可能讓原本通過的案例失敗；收緊任一上限、或提高併發（引入競態）才會。
func TestNeedsVerification(t *testing.T) {
	cur := Knobs{MaxTurns: 40, MaxConcurrentTools: 5, MaxCostUSD: 1.0}

	cases := []struct {
		name string
		ps   []Proposal
		want bool
	}{
		{"收緊回合上限 → 要驗", []Proposal{{Knob: "max_turns", Proposed: 20}}, true},
		{"收緊成本上限 → 要驗", []Proposal{{Knob: "max_cost_usd", Proposed: 0.5}}, true},
		{"提高併發（競態）→ 要驗", []Proposal{{Knob: "max_concurrent_tools", Proposed: 8}}, true},
		{"放寬回合上限 → 免驗", []Proposal{{Knob: "max_turns", Proposed: 60}}, false},
		{"放寬成本上限 → 免驗", []Proposal{{Knob: "max_cost_usd", Proposed: 3.0}}, false},
		{"降低併發 → 免驗", []Proposal{{Knob: "max_concurrent_tools", Proposed: 2}}, false},
		{"混合：只要有一個收緊就要驗", []Proposal{
			{Knob: "max_turns", Proposed: 20}, {Knob: "max_cost_usd", Proposed: 3.0},
		}, true},
	}
	for _, c := range cases {
		got, reason := NeedsVerification(cur, c.ps)
		if got != c.want {
			t.Errorf("%s：預期 %v 實得 %v", c.name, c.want, got)
		}
		if !got && reason == "" {
			t.Errorf("%s：免驗時應說明原因", c.name)
		}
	}
}

func TestCandidateKnobs(t *testing.T) {
	cur := Knobs{MaxTurns: 40, MaxConcurrentTools: 5, MaxCostUSD: 1.0}
	cand := CandidateKnobs(cur, []Proposal{
		{Knob: "max_turns", Proposed: 25},
		{Knob: "max_cost_usd", Proposed: 2.5},
	})
	if cand.MaxTurns != 25 || cand.MaxCostUSD != 2.5 {
		t.Errorf("提案應套到候選值上，實得 %+v", cand)
	}
	if cand.MaxConcurrentTools != 5 {
		t.Errorf("未提案的旋鈕應保留現行值，實得 %d", cand.MaxConcurrentTools)
	}
}

// 驗證證據必須連【樣本數】一起落地：只有 2 個用例時通過率粒度是 50%，
// 分不出回歸與雜訊。不標低信心的話，人會把沒有意義的綠燈當成保證。
func TestVerification_PersistsEvidenceWithSampleSize(t *testing.T) {
	dir := t.TempDir()
	v := Verification{
		Ran: true, SampleSize: 2,
		BaselinePassRate: 1.0, CandidatePassRate: 0.5,
		Regressed: true, LowConfidence: true,
	}
	path, err := WriteProposedConfig(dir, Knobs{MaxTurns: 40}, []Proposal{{Knob: "max_turns", Proposed: 20}}, v)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Verification Verification `json:"verification"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	got := doc.Verification
	if !got.Ran || got.SampleSize != 2 || !got.Regressed || !got.LowConfidence {
		t.Errorf("驗證證據未完整落地：%+v", got)
	}
	if got.BaselinePassRate != 1.0 || got.CandidatePassRate != 0.5 {
		t.Errorf("前後通過率應原樣保留：%+v", got)
	}
	_ = filepath.Base(path)
}
