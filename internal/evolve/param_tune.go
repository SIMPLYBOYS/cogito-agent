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

// ActiveConfigFileName 是【已套用】的執行期參數檔。引擎啟動時讀它覆蓋預設旋鈕；由 apply config 從
// 提案檔晉升而來（propose→人工 apply→生效，與 memory/edges 同一閘）。
const ActiveConfigFileName = "config.json"

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

// LoadKnobs 讀【已套用】的執行期參數 <root>/.claw/config.json。bool 表示存在且可解析——不存在時
// 呼叫端沿用引擎預設（不套用）。這是讓調參提案「執行期可讀」的那一端，閉合 tune→propose→apply→生效。
func LoadKnobs(root string) (Knobs, bool) {
	data, err := os.ReadFile(filepath.Join(root, ".claw", ActiveConfigFileName))
	if err != nil {
		return Knobs{}, false
	}
	var k Knobs
	if json.Unmarshal(data, &k) != nil {
		return Knobs{}, false
	}
	return k, true
}

// ApplyProposedConfig 把提案參數過【人工閘】套用：讀 config.proposed.json，以其 current 為基底套上每條
// proposal 的 proposed 值，寫入 config.json（引擎啟動時會讀），並刪除提案檔。回傳每條「knob old→new」摘要。
// 套用時一律再 clamp 在有界範圍（雙保險——即便有人手改提案檔也繞不過安全上限）。無提案回 (nil, nil)。
func ApplyProposedConfig(root string) ([]string, error) {
	clawDir := filepath.Join(root, ".claw")
	proposedPath := filepath.Join(clawDir, ProposedConfigFileName)
	data, err := os.ReadFile(proposedPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Current   Knobs      `json:"current"`
		Proposals []Proposal `json:"proposals"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析提案參數失敗: %w", err)
	}

	k := doc.Current // 以提案基底為起點，套上各提案值
	var changes []string
	for _, p := range doc.Proposals {
		switch p.Knob {
		case "max_turns":
			v := clampInt(int(toFloat(p.Proposed)), minTurns, maxTurns)
			changes = append(changes, fmt.Sprintf("max_turns: %d→%d", k.MaxTurns, v))
			k.MaxTurns = v
		case "max_concurrent_tools":
			v := clampInt(int(toFloat(p.Proposed)), minConcurrency, maxConcurrency)
			changes = append(changes, fmt.Sprintf("max_concurrent_tools: %d→%d", k.MaxConcurrentTools, v))
			k.MaxConcurrentTools = v
		case "max_cost_usd":
			v := clampFloat(toFloat(p.Proposed), minCostUSD, maxCostUSD)
			changes = append(changes, fmt.Sprintf("max_cost_usd: %.2f→%.2f", k.MaxCostUSD, v))
			k.MaxCostUSD = v
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}

	// 對【整份】結果 clamp，不能只 clamp 各提案值：起點 k 來自 doc.Current，那是【磁碟上的提案檔】
	// 讀來的，同樣不可信。只 clamp 提案的話，一個塞了 {"current":{"max_cost_usd":99999}} 的提案檔
	// 配上任一個合法提案，就能讓 99999 原封不動落進 config.json——成本熔斷實質關閉。
	// 信任邊界是「寫進磁碟的東西不可信」，不是「提案不可信」。
	// 只 clamp【已設值】：0 的語意是「不覆蓋，用引擎預設」（見 cmd/claw/main.go 的 `if k.X > 0`），
	// 無條件 clamp 會把「不覆蓋」悄悄變成「max_turns=10 / max_cost=$0.5」，反而收緊了限制。
	if k.MaxTurns != 0 {
		k.MaxTurns = clampInt(k.MaxTurns, minTurns, maxTurns)
	}
	if k.MaxConcurrentTools != 0 {
		k.MaxConcurrentTools = clampInt(k.MaxConcurrentTools, minConcurrency, maxConcurrency)
	}
	if k.MaxCostUSD != 0 {
		k.MaxCostUSD = clampFloat(k.MaxCostUSD, minCostUSD, maxCostUSD)
	}

	if err := os.MkdirAll(clawDir, 0o755); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(clawDir, ActiveConfigFileName), out, 0o644); err != nil {
		return nil, fmt.Errorf("寫入 config.json 失敗: %w", err)
	}
	_ = os.Remove(proposedPath) // 套用後清掉提案（與 memory/edges 一致）
	return changes, nil
}

// DiscardProposedConfig 丟棄提案參數（未套用）。回傳原本是否有提案。
func DiscardProposedConfig(root string) (bool, error) {
	p := filepath.Join(root, ".claw", ProposedConfigFileName)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return false, nil
	}
	return true, os.Remove(p)
}

// toFloat 把 JSON 解出的提案值（數字一律是 float64）轉成 float64。
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
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
