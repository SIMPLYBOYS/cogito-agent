package evolve

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

// ProposedConfigFileName 是「提案參數」暫存檔（位於 .claw/ 下）。參數自調只寫這裡，
// 絕不自動改 live 旋鈕——同一安全鐵律：放寬任何「失控控制」旋鈕都須人工審核後手動套用。
const ProposedConfigFileName = "config.proposed.json"

// Knobs 是引擎可調的「失控控制」旋鈕（對應 internal/engine 的預設）。
type Knobs struct {
	MaxTurns           int     `json:"max_turns"`
	MaxConcurrentTools int     `json:"max_concurrent_tools"`
	MaxCostUSD         float64 `json:"max_cost_usd"`
}

// RunStats 是一次跑分的聚合觀測（由呼叫方從 SuiteReport 算好傳入，避免 evolve 依賴 eval 套件）。
type RunStats struct {
	N              int
	PassRate       float64
	MeanTurns      float64
	MeanToolErrors float64
	MaxCaseCostUSD float64
	CeilingHitRate float64 // 失敗且觸頂回合上限的案例占比
}

// Proposal 是一條調參提案。Sensitive=true 代表「放寬安全旋鈕」，需特別審慎。
type Proposal struct {
	Knob      string `json:"knob"`
	Current   any    `json:"current"`
	Proposed  any    `json:"proposed"`
	Reason    string `json:"reason"`
	Sensitive bool   `json:"sensitive"`
}

// 有界範圍：所有提案一律 clamp 在這些區間內，杜絕「把安全旋鈕開到無限大」。
const (
	minConcurrency = 1
	maxConcurrency = 8
	minTurns       = 10
	maxTurns       = 60
	minCostUSD     = 0.5
	maxCostUSD     = 5.0
)

// Advise 依聚合觀測產出【確定性】調參提案（不交給 LLM 決定安全旋鈕）。提案一律有界，且放寬安全
// 旗標（提高上限）會標 Sensitive。回傳空＝目前參數看起來合適。
func Advise(s RunStats, cur Knobs) []Proposal {
	var ps []Proposal
	if s.N == 0 {
		return ps
	}

	// 1) 工具報錯偏高 → 降併發（減少競態/重試）。收緊方向，安全。
	if s.MeanToolErrors >= 3 && cur.MaxConcurrentTools > minConcurrency {
		ps = append(ps, Proposal{
			Knob: "max_concurrent_tools", Current: cur.MaxConcurrentTools,
			Proposed: clampInt(cur.MaxConcurrentTools-1, minConcurrency, maxConcurrency),
			Reason:   fmt.Sprintf("工具平均報錯 %.1f 次偏高，降併發以減少競態與重試", s.MeanToolErrors),
		})
	}

	// 2) 通過率高且平均回合遠低於上限 → 收緊回合上限以更早暴露低效。收緊方向，安全。
	if s.PassRate >= 0.9 && cur.MaxTurns > minTurns && s.MeanTurns > 0 && s.MeanTurns < float64(cur.MaxTurns)/3 {
		tighter := clampInt(int(math.Ceil(s.MeanTurns*2)), minTurns, cur.MaxTurns-1)
		if tighter < cur.MaxTurns {
			ps = append(ps, Proposal{
				Knob: "max_turns", Current: cur.MaxTurns, Proposed: tighter,
				Reason: fmt.Sprintf("通過率 %.0f%%、平均僅 %.1f 回合（遠低於上限 %d），可收緊以更早暴露低效", s.PassRate*100, s.MeanTurns, cur.MaxTurns),
			})
		}
	}

	// 3) 失敗案例多觸頂回合上限 → 上限可能過低，謹慎【放寬】（敏感）。
	if s.CeilingHitRate >= 0.3 && cur.MaxTurns < maxTurns {
		ps = append(ps, Proposal{
			Knob: "max_turns", Current: cur.MaxTurns,
			Proposed:  clampInt(cur.MaxTurns+cur.MaxTurns/4, minTurns, maxTurns),
			Reason:    fmt.Sprintf("%.0f%% 失敗案例觸頂回合上限，疑上限過低；放寬前請確認非任務本身過難", s.CeilingHitRate*100),
			Sensitive: true,
		})
	}

	// 4) 最貴案例逼近成本熔斷上限 → 謹慎【放寬】（敏感）。
	if cur.MaxCostUSD > 0 && s.MaxCaseCostUSD > 0.8*cur.MaxCostUSD && cur.MaxCostUSD < maxCostUSD {
		ps = append(ps, Proposal{
			Knob: "max_cost_usd", Current: cur.MaxCostUSD,
			Proposed:  clampFloat(cur.MaxCostUSD*1.5, minCostUSD, maxCostUSD),
			Reason:    fmt.Sprintf("最貴案例 $%.4f 逼近成本熔斷上限 $%.2f；放寬會提高單次燒錢風險", s.MaxCaseCostUSD, cur.MaxCostUSD),
			Sensitive: true,
		})
	}

	return ps
}

// WriteProposedConfig 把提案寫進【暫存】config.proposed.json（不自動套用）。
func WriteProposedConfig(clawDir string, cur Knobs, proposals []Proposal) (string, error) {
	if err := os.MkdirAll(clawDir, 0o755); err != nil {
		return "", fmt.Errorf("建立 .claw 目錄失敗: %w", err)
	}
	path := filepath.Join(clawDir, ProposedConfigFileName)
	doc := map[string]any{
		"note":         "⚠️ 自動產出的調參提案，僅供參考。須人工 review 後手動套用（改 engine 預設），不會自動生效；放寬安全旋鈕（sensitive=true）請特別審慎。",
		"generated_at": time.Now().Format(time.RFC3339),
		"current":      cur,
		"proposals":    proposals,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("寫入提案參數失敗: %w", err)
	}
	return path, nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
