package eval

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
)

// A/B 消融跑一次只有 n=1，下不了結論——先前技能 A/B 的 p=0.206 是【手算】的，程式裡沒有。
// 手算跨 20 次 stdout 計票正是 docs/eval-results.md 記錄過的那類方法論錯誤，所以聚合與顯著性
// 檢定都放進工具本身：跑完直接吐 2×2 表、Fisher 精確檢定 p 值、以及成本/回合的中位數。
//
// ponytail: Fisher 精確檢定而非卡方——樣本小（n=20、格子數個位數）時卡方的常態近似不成立，
// Fisher 是離散精確解、沒有近似誤差。兩者程式碼量差不多，就沒有理由選會在小樣本失準的那個。

// ABAggregate 是 n 次 A/B 配對跑的聚合結果。
type ABAggregate struct {
	Model   string  `json:"model"`
	N       int     `json:"n"`
	OffPass int     `json:"off_pass"`
	OnPass  int     `json:"on_pass"`
	PValue  float64 `json:"p_value"` // 雙尾 Fisher 精確檢定
	// LowConfidence＝樣本數低於 evolve.MinVerifySamples。與 p 值【獨立】：小樣本下
	// 就算碰巧 p<0.05 也不該當結論——通過率的粒度是 1/N，n=3 時一個案例翻掉就是 33% 落差，
	// 而 LLM 本身有非確定性，分不出那是效應還是雜訊。沿用既有門檻，不另立一套。
	LowConfidence bool     `json:"low_confidence"`
	Pairs         []ABPair `json:"pairs"`
}

// ABPair 是一次配對（同任務、同模型，只切換受測功能）。
type ABPair struct {
	Off ABRun `json:"off"`
	On  ABRun `json:"on"`
}

// fisherExact2x2 回傳雙尾 Fisher 精確檢定的 p 值。表格：
//
//	          通過   失敗
//	off        a      b
//	on         c      d
//
// 作法是列舉所有在相同邊際和下可能的表，把「機率 ≤ 觀察表機率」的都加起來
// （雙尾的標準定義，與 R 的 fisher.test 及 scipy 一致）。
func fisherExact2x2(a, b, c, d int) float64 {
	obs := hypergeomProb(a, b, c, d)
	rowOff, total := a+b, a+b+c+d
	colPass := a + c
	lo := max(0, colPass-(c+d))
	hi := min(rowOff, colPass)
	var p float64
	for x := lo; x <= hi; x++ {
		// 固定邊際和，x 決定整張表
		xa, xb := x, rowOff-x
		xc, xd := colPass-x, total-rowOff-(colPass-x)
		if xb < 0 || xc < 0 || xd < 0 {
			continue
		}
		if q := hypergeomProb(xa, xb, xc, xd); q <= obs+1e-12 {
			p += q
		}
	}
	return math.Min(1, p)
}

// hypergeomProb 是給定邊際和下該表的超幾何機率。走 log 階乘避免大數溢位。
func hypergeomProb(a, b, c, d int) float64 {
	n := a + b + c + d
	logP := lnFact(a+b) + lnFact(c+d) + lnFact(a+c) + lnFact(b+d) -
		lnFact(a) - lnFact(b) - lnFact(c) - lnFact(d) - lnFact(n)
	return math.Exp(logP)
}

func lnFact(n int) float64 {
	if n < 2 {
		return 0
	}
	// math.Lgamma(n+1) = ln(n!)
	v, _ := math.Lgamma(float64(n + 1))
	return v
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

// Aggregate 把 n 次配對算成 2×2 表 + Fisher p 值。
func Aggregate(model string, pairs []ABPair) *ABAggregate {
	agg := &ABAggregate{Model: model, N: len(pairs), Pairs: pairs}
	for _, p := range pairs {
		if p.Off.Passed {
			agg.OffPass++
		}
		if p.On.Passed {
			agg.OnPass++
		}
	}
	agg.PValue = fisherExact2x2(agg.OffPass, agg.N-agg.OffPass, agg.OnPass, agg.N-agg.OnPass)
	agg.LowConfidence = agg.N < evolve.MinVerifySamples
	return agg
}

// Render 印出可直接貼進 eval-results.md 的摘要。刻意把「未達顯著」講白，
// 不讓讀者從 p 值自己推——先前 README 就是因為只寫通過率而讓一個未達顯著的觀察讀起來像結論。
func (a *ABAggregate) Render(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s | 模型 %s | n=%d\n", title, a.Model, a.N)
	fmt.Fprintf(&b, "%-6s %10s %12s %12s\n", "條件", "通過率", "回合中位數", "成本中位數")

	row := func(label string, pass int, pick func(ABPair) ABRun) {
		var turns, costs []float64
		for _, p := range a.Pairs {
			r := pick(p)
			turns = append(turns, float64(r.TurnCount))
			costs = append(costs, r.TotalCostUSD)
		}
		fmt.Fprintf(&b, "%-6s %4d/%-5d %12.1f %12.4f\n",
			label, pass, a.N, median(turns), median(costs))
	}
	row("off", a.OffPass, func(p ABPair) ABRun { return p.Off })
	row("on", a.OnPass, func(p ABPair) ABRun { return p.On })

	fmt.Fprintf(&b, "\nFisher 精確檢定（雙尾）p = %.4f", a.PValue)
	switch {
	case a.LowConfidence:
		// 樣本門檻先於 p 值：n 太小時 p 值本身就不穩，"達顯著" 會給出假的安心感。
		fmt.Fprintf(&b, " → **樣本不足**（n=%d < %d），無論 p 值多少都只能當觀察\n",
			a.N, evolve.MinVerifySamples)
	case a.PValue < 0.05:
		fmt.Fprintf(&b, " → **達顯著**（α=0.05），可以當結論\n")
	default:
		fmt.Fprintf(&b, " → **未達顯著**（α=0.05），只能當觀察，不可寫成結論\n")
	}
	return b.String()
}
