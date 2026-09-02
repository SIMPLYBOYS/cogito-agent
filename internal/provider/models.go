package provider

// 「現在真正能用哪些模型」——問官方，不維護一張會過期的表。
//
// 【為何需要】先前唯一的模型清單是 observability.PricingModel（計價用，手動維護）。
// 實測打過官方 /v1/models：那時表裡缺 opus-5、sonnet-5，而 persona 已經在用 opus-5，
// 於是它的花費走 fallback 估價——數字碰巧接近，但那是運氣。手動表必然落後於發布。
//
// 這裡只回「有哪些、叫什麼」，不回價格：價格官方 API 不提供，仍由 PricingModel 負責
// （而未登記的花費會被標成估價，見 office 協定的 cost_est）。

import (
	"context"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// ModelInfo 是一個可用模型的最小描述。刻意不照抄 SDK 的結構——那會讓上層綁死在某家 SDK 上。
type ModelInfo struct {
	ID   string `json:"id"`   // API 用的 id，如 claude-opus-5 或 claude-haiku-4-5-20251001
	Name string `json:"name"` // 人看的名字，如 "Claude Opus 5"
}

// ModelLister 是「能列出可用模型」的【可選】能力。provider 不支援就不實作——
// 呼叫端用型別斷言判斷，拿不到就降級（清單是加值，不是前提）。
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ListModels 問 Anthropic 的 /v1/models。SDK 自己處理分頁。
func (p *ClaudeProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var out []ModelInfo
	it := p.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	for it.Next() {
		m := it.Current()
		out = append(out, ModelInfo{ID: m.ID, Name: m.DisplayName})
	}
	return out, it.Err()
}

// modelsTTL 是清單的快取時效。模型發布是以週計的事，問太勤只是浪費——
// 但也不能只問一次：bot 是常駐行程，跑上幾週不重啟很正常。
const modelsTTL = 6 * time.Hour

// CachedLister 給 ListModels 加一層 TTL 快取。取不到新的就沿用舊的（過期也照給）——
// 一份稍舊的清單，遠比因為網路抖一下就變空白有用。
type CachedLister struct {
	inner ModelLister
	mu    sync.Mutex
	cache []ModelInfo
	at    time.Time
}

func NewCachedLister(inner ModelLister) *CachedLister { return &CachedLister{inner: inner} }

func (c *CachedLister) ListModels(ctx context.Context) ([]ModelInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache != nil && time.Since(c.at) < modelsTTL {
		return c.cache, nil
	}
	got, err := c.inner.ListModels(ctx)
	if err != nil {
		if c.cache != nil {
			return c.cache, nil // 有舊的就用舊的：清單稍舊 ≫ 因為一次網路失敗就空白
		}
		return nil, err
	}
	c.cache, c.at = got, time.Now()
	return got, nil
}
