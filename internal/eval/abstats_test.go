package eval

import (
	"math"
	"strings"
	"testing"
)

// 對照 R 的 fisher.test / scipy.stats.fisher_exact 的已知值。第一筆就是 docs/eval-results.md
// 裡那個【手算】的 p=0.206——程式算出同一個數，才證明我們沒有換了一套算法自我安慰。
func TestFisherExact2x2(t *testing.T) {
	cases := []struct {
		name       string
		a, b, c, d int
		want       float64
	}{
		// 這筆是關鍵：docs/eval-results.md 裡的 p=0.206 是【手算】的，程式算出同一個數
		// 才證明我們沒換一套算法自我安慰。
		{"技能 A/B 現況 1/5 vs 4/5（先前手算 0.206）", 1, 4, 4, 1, 0.2063492},
		// 教科書值：Fisher 品茶實驗完全分離＝2/C(10,5)=2/252。
		{"完全分離 0/5 vs 5/5", 0, 5, 5, 0, 0.0079365},
		{"完全無差異 2/2 vs 2/2", 2, 2, 2, 2, 1.0},
		{"n=20 假想 8/20 vs 17/20", 8, 12, 17, 3, 0.0079117},
	}
	for _, c := range cases {
		got := fisherExact2x2(c.a, c.b, c.c, c.d)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%s: fisher(%d,%d,%d,%d) = %.7f, want %.7f", c.name, c.a, c.b, c.c, c.d, got, c.want)
		}
	}
}

func TestFisherSymmetryAndBounds(t *testing.T) {
	// 交換兩列（off/on 對調）不該改變雙尾 p 值
	if x, y := fisherExact2x2(1, 4, 4, 1), fisherExact2x2(4, 1, 1, 4); math.Abs(x-y) > 1e-12 {
		t.Errorf("列對調 p 值應相同：%.7f vs %.7f", x, y)
	}
	// p 永遠在 (0,1]
	for _, c := range [][4]int{{0, 20, 20, 0}, {10, 10, 10, 10}, {1, 19, 2, 18}} {
		p := fisherExact2x2(c[0], c[1], c[2], c[3])
		if p <= 0 || p > 1+1e-12 {
			t.Errorf("fisher%v = %v，超出 (0,1]", c, p)
		}
	}
}

// Render 必須把「未達顯著」講白——先前 README 只寫通過率，讓一個 p=0.206 的觀察讀起來像結論。
func TestAggregateRenderStatesSignificance(t *testing.T) {
	mk := func(offPass, onPass, n int) []ABPair {
		var ps []ABPair
		for i := range n {
			ps = append(ps, ABPair{
				Off: ABRun{Label: "off", Passed: i < offPass, TurnCount: 4, TotalCostUSD: 0.01},
				On:  ABRun{Label: "on", Passed: i < onPass, TurnCount: 5, TotalCostUSD: 0.02},
			})
		}
		return ps
	}

	// 計票正確性
	weak := Aggregate("m", mk(1, 4, 5))
	if weak.OffPass != 1 || weak.OnPass != 4 || weak.N != 5 {
		t.Fatalf("計票錯了: %+v", weak)
	}

	// 樣本【夠】但效應不顯著 → 才會走「未達顯著」這條。n=5 那條走的是樣本不足，
	// 見 TestLowConfidenceOverridesSignificance。
	ns := Aggregate("m", mk(8, 11, 20))
	if ns.LowConfidence {
		t.Fatalf("n=20 不該標低信心")
	}
	out := ns.Render("技能 A/B")
	if !strings.Contains(out, "未達顯著") || !strings.Contains(out, "不可寫成結論") {
		t.Errorf("p=%.4f 應標為未達顯著:\n%s", ns.PValue, out)
	}

	strong := Aggregate("m", mk(4, 18, 20))
	if o := strong.Render("技能 A/B"); !strings.Contains(o, "達顯著") || strings.Contains(o, "未達顯著") {
		t.Errorf("p=%.4f 應標為達顯著:\n%s", strong.PValue, o)
	}
}

func TestMedian(t *testing.T) {
	if got := median([]float64{3, 1, 2}); got != 2 {
		t.Errorf("奇數個: got %v want 2", got)
	}
	if got := median([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Errorf("偶數個: got %v want 2.5", got)
	}
	if got := median(nil); got != 0 {
		t.Errorf("空: got %v want 0", got)
	}
}

// 樣本門檻要【先於】p 值：n=3 碰巧 p<0.05 也不該印「達顯著」——通過率粒度是 1/N，
// 小樣本下一個案例翻掉就是巨大落差，而 LLM 本身有非確定性。沿用 evolve.MinVerifySamples。
func TestLowConfidenceOverridesSignificance(t *testing.T) {
	mk := func(offPass, onPass, n int) []ABPair {
		var ps []ABPair
		for i := range n {
			ps = append(ps, ABPair{
				Off: ABRun{Passed: i < offPass},
				On:  ABRun{Passed: i < onPass},
			})
		}
		return ps
	}

	// n=3 完全分離：p 會很小，但樣本不足，必須擋在 p 值前面
	tiny := Aggregate("m", mk(0, 3, 3))
	if !tiny.LowConfidence {
		t.Fatalf("n=3 應標低信心")
	}
	out := tiny.Render("t")
	if !strings.Contains(out, "樣本不足") {
		t.Errorf("n=3 p=%.4f 應印樣本不足:\n%s", tiny.PValue, out)
	}
	if strings.Contains(out, "可以當結論") {
		t.Errorf("樣本不足時不可說可以當結論:\n%s", out)
	}

	// n=20 才允許出現「達顯著」
	ok := Aggregate("m", mk(4, 18, 20))
	if ok.LowConfidence {
		t.Errorf("n=20 不該標低信心")
	}
	if o := ok.Render("t"); !strings.Contains(o, "達顯著") || strings.Contains(o, "樣本不足") {
		t.Errorf("n=20 且 p=%.4f 應可下結論:\n%s", ok.PValue, o)
	}
}
