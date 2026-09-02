package observability

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func writePricing(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".claw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PricingFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func resetPricing(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		pricingMu.Lock()
		pricingRoot, custom, pricingRead, pricingAt = "", nil, false, time.Time{}
		pricingMu.Unlock()
	})
}

// 新模型的單價應該是【改資料】不是改程式：放一個 pricing.json 就登記好了。
func TestPricingFileRegistersNewModel(t *testing.T) {
	resetPricing(t)
	root := t.TempDir()
	writePricing(t, root, `{"claude-fable-5-1": {"input": 10, "output": 50}}`)
	SetPricingRoot(root)

	if !IsRegistered("claude-fable-5-1") {
		t.Fatal("檔案裡登記的模型應算已登記（不必改程式）")
	}
	// 一百萬輸入 token = $10（不是 fallback 的 $5）
	if got := CostOf("claude-fable-5-1", schema.Usage{PromptTokens: 1_000_000}); got != 10.0 {
		t.Errorf("單價沒吃到檔案：$%.4f，want $10.0000", got)
	}
	// 內建表照樣有效（檔案是疊加，不是取代）
	if !IsRegistered("claude-opus-5") {
		t.Error("內建表不該被檔案取代掉")
	}
}

// 檔案可以【覆蓋】內建價（實際會用到：官方調價、或走的是折扣合約）。
func TestPricingFileOverridesBuiltin(t *testing.T) {
	resetPricing(t)
	root := t.TempDir()
	writePricing(t, root, `{"claude-haiku-4-5": {"input": 0.5, "output": 2.5}}`)
	SetPricingRoot(root)
	if got := CostOf("claude-haiku-4-5", schema.Usage{PromptTokens: 1_000_000}); got != 0.5 {
		t.Errorf("覆蓋沒生效：$%.4f，want $0.5000（內建是 $1）", got)
	}
	// 日期尾綴的寫法也要吃到同一筆
	if got := CostOf("claude-haiku-4-5-20251001", schema.Usage{PromptTokens: 1_000_000}); got != 0.5 {
		t.Errorf("帶日期的 id 沒吃到覆蓋：$%.4f", got)
	}
}

// 【最重要的一條】0 或負數單價不得被接受——它會讓成本永遠不累積，
// 等於默默關掉 MaxCostUSD 熔斷。寧可走 fallback 估價（會被標成 ~）。
func TestPricingRejectsZeroPrice(t *testing.T) {
	resetPricing(t)
	root := t.TempDir()
	writePricing(t, root, `{"free-model": {"input": 0, "output": 0},
	                        "neg-model": {"input": -1, "output": -1},
	                        "ok-model": {"input": 2, "output": 8}}`)
	SetPricingRoot(root)
	for _, m := range []string{"free-model", "neg-model"} {
		if IsRegistered(m) {
			t.Errorf("%s 的 0/負單價不該被接受——那會讓成本熔斷失效", m)
		}
		if got := CostOf(m, schema.Usage{PromptTokens: 1_000_000}); got != fallbackInputPrice {
			t.Errorf("%s 應走 fallback 估價，got $%.4f", m, got)
		}
	}
	if !IsRegistered("ok-model") {
		t.Error("同一個檔裡合法的那筆仍要生效（壞的略過，不是整檔作廢）")
	}
}

// 壞掉的檔要沿用上一份好的：改壞檔案的當下，不該讓正在跑的任務算錯錢。
func TestPricingKeepsLastGoodOnBadFile(t *testing.T) {
	resetPricing(t)
	root := t.TempDir()
	writePricing(t, root, `{"m": {"input": 3, "output": 9}}`)
	SetPricingRoot(root)
	if got := CostOf("m", schema.Usage{PromptTokens: 1_000_000}); got != 3.0 {
		t.Fatalf("前置條件不成立：$%.4f", got)
	}
	time.Sleep(10 * time.Millisecond) // 讓 mtime 真的變
	writePricing(t, root, `{ 這不是 JSON `)
	if got := CostOf("m", schema.Usage{PromptTokens: 1_000_000}); got != 3.0 {
		t.Errorf("檔案壞掉時應沿用上一份好的，got $%.4f", got)
	}
}

// 沒有檔案＝行為與先前完全相同（只用內建表）。
func TestPricingWithoutFile(t *testing.T) {
	resetPricing(t)
	SetPricingRoot(t.TempDir())
	if !IsRegistered("claude-opus-5") || IsRegistered("不存在的模型") {
		t.Error("沒有 pricing.json 時應完全退回內建表")
	}
}

// 熱更新：改了檔案就生效，不必重啟（與 config.json 同款——它是每次建引擎都重讀的）。
// 沒有這條，前面「壞檔沿用舊值」的測試分不出「有重讀但守住了」與「根本沒重讀」。
func TestPricingHotReloads(t *testing.T) {
	resetPricing(t)
	root := t.TempDir()
	writePricing(t, root, `{"m": {"input": 3, "output": 9}}`)
	SetPricingRoot(root)
	if got := CostOf("m", schema.Usage{PromptTokens: 1_000_000}); got != 3.0 {
		t.Fatalf("前置條件不成立：$%.4f", got)
	}
	time.Sleep(10 * time.Millisecond) // 讓 mtime 真的變
	writePricing(t, root, `{"m": {"input": 7, "output": 21}}`)
	if got := CostOf("m", schema.Usage{PromptTokens: 1_000_000}); got != 7.0 {
		t.Errorf("改了檔案應立刻生效，got $%.4f（還是舊的 $3 → 沒有重讀）", got)
	}
}
