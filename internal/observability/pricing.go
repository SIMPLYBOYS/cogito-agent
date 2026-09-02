package observability

// 單價從「改程式」變成「改資料」。
//
// 【為何需要】官方的 /v1/models 回得出有哪些模型、窗口多大，就是【不回價格】（實測確認過
// ModelInfo 的欄位：id/capabilities/created_at/display_name/max_input_tokens/max_tokens/type）。
// 所以價格只能自己維護——但沒有理由每次新模型發布都要改 Go 檔、重編 binary。
//
// 內建表（PricingModel）仍是預設值，開箱即用；<root>/.claw/pricing.json 疊在它上面，
// 可以覆蓋既有的、也可以補新的。檔案改了就生效，不必重啟（跟 config.json 同款）。
//
// 【為何放 .claw/】那個目錄的檔案工具寫入已經被擋掉（tools/path.go 的 controlDir）。
// 價格屬於同一類東西：能改價就等於能把成本熔斷歸零——把單價設成 0，MaxCostUSD 永遠不會觸發。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PricingFileName 是自訂單價檔（位於 <root>/.claw/ 下）。
const PricingFileName = "pricing.json"

// Price 是每百萬 token 的美元單價。欄位名用 input/output 而不是 InputPrice/OutputPrice——
// 這個檔是給人手改的，短名字比較好打，也比較不會拼錯。
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

var (
	pricingMu   sync.RWMutex
	pricingRoot string
	custom      map[string]Price
	pricingAt   time.Time // 上次讀到的檔案 mtime；用它判斷要不要重讀
	pricingRead bool      // 已經試著讀過（含檔案不存在）——避免每次呼叫都 stat 不存在的檔
)

// SetPricingRoot 指定自訂單價檔的所在（<root>/.claw/pricing.json），並立刻讀一次。
// 各入口啟動時呼叫；不呼叫＝只用內建表（行為與先前完全相同）。
func SetPricingRoot(root string) {
	pricingMu.Lock()
	pricingRoot, pricingRead = root, false
	pricingMu.Unlock()
	reloadPricing()
}

func pricingPath() string {
	if pricingRoot == "" {
		return ""
	}
	return filepath.Join(pricingRoot, ".claw", PricingFileName)
}

// reloadPricing 在檔案有變動時重讀。壞掉的檔【保留上一份好的】——半套價格比舊價格危險，
// 而且改壞檔案的當下不該讓正在跑的任務算錯錢。
func reloadPricing() {
	pricingMu.Lock()
	defer pricingMu.Unlock()
	p := pricingPath()
	if p == "" {
		return
	}
	st, err := os.Stat(p)
	if err != nil {
		if !pricingRead {
			pricingRead = true // 檔案不存在很正常（只用內建表），別每次呼叫都吵
		}
		return
	}
	if pricingRead && st.ModTime().Equal(pricingAt) {
		return // 沒動過
	}
	pricingRead, pricingAt = true, st.ModTime()
	data, err := os.ReadFile(p)
	if err != nil {
		log.Printf("[pricing] 讀不到 %s（沿用現有單價）: %v", p, err)
		return
	}
	var m map[string]Price
	if err := json.Unmarshal(data, &m); err != nil {
		log.Printf("[pricing] %s 格式錯誤（沿用現有單價）: %v", p, err)
		return
	}
	// 單價必須是正數：0 或負數會讓成本永遠不累積，等於默默關掉 MaxCostUSD 熔斷。
	// 寧可略過那一筆走 fallback 估價（會被標成 ~），也不要接受一個讓熔斷失效的值。
	clean := map[string]Price{}
	for id, pr := range m {
		if pr.Input <= 0 || pr.Output <= 0 {
			log.Printf("[pricing] 略過 %q：單價必須為正數（in=%v out=%v）——0 會讓成本熔斷失效", id, pr.Input, pr.Output)
			continue
		}
		clean[id] = pr
	}
	custom = clean
	log.Printf("[pricing] 已套用 %d 筆自訂單價（%s）", len(clean), p)
}

// priceOf 查單價：自訂檔優先於內建表；兩者都吃日期尾綴正規化（見 modelKey）。
func priceOf(model string) (Price, bool) {
	reloadPricing() // 檔案改了就生效，不必重啟（與 config.json 同款）
	key := modelKey(model)
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	if p, ok := custom[model]; ok {
		return p, true
	}
	if p, ok := custom[key]; ok {
		return p, true
	}
	if p, ok := PricingModel[key]; ok {
		return Price{Input: p.InputPrice, Output: p.OutputPrice}, true
	}
	return Price{}, false
}
