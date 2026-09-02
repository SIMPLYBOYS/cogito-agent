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
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// ModelInfo 是一個可用模型的最小描述。刻意不照抄 SDK 的結構——那會讓上層綁死在某家 SDK 上。
type ModelInfo struct {
	ID   string `json:"id"`   // API 用的 id，如 claude-opus-5 或 claude-haiku-4-5-20251001
	Name string `json:"name"` // 人看的名字，如 "Claude Opus 5"
	// Window 是這個模型的真實輸入窗口（max_input_tokens）。壓縮水位靠它算——
	// 寫死一個數字會在窗口變大時默默浪費、在變小時直接讓任務失敗。
	Window int `json:"window,omitempty"`
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
		out = append(out, ModelInfo{ID: m.ID, Name: m.DisplayName, Window: int(m.MaxInputTokens)})
	}
	return out, it.Err()
}

// modelsTTL 是清單的快取時效。模型發布是以週計的事，問太勤只是浪費——
// 但也不能只問一次：bot 是常駐行程，跑上幾週不重啟很正常。
const modelsTTL = 6 * time.Hour

// windowFallback 是查不到真實窗口時的保守值。
//
// 【方向很重要】猜低只是浪費（提早壓縮），猜高會讓任務直接失敗（超過窗口被 API 拒絕）。
// 所以未知時一律往低猜，只有拿到官方明確數字才調高。
const windowFallback = 200000

// datedID 與 observability 那支同樣的日期尾綴（claude-haiku-4-5-20251001）。刻意重寫一份
// 而不是共用：observability 依賴 provider，反向 import 會成環。三行的東西不值得為它拆包。
var datedID = regexp.MustCompile(`-\d{8}$`)

// 模型 → 實際輸入窗口。package 層而非實例層：Configure 會為每個子 agent 複製出新的
// provider（各自帶不同 model），實例層快取等於每個子 agent 各查一次。
var (
	windowsMu      sync.Mutex
	windows        map[string]int
	windowsAt      time.Time
	windowsLoading bool // 防止同時多個任務各抓一次
)

// warmWindows 在建 provider 時就背景抓一次。沒有它，第一個任務會用保守值——
// 不會壞事，但那正是我們要修掉的浪費，能提早幾秒就別留著。
func (p *ClaudeProvider) warmWindows() {
	windowsMu.Lock()
	defer windowsMu.Unlock()
	if windowsLoading || windows != nil {
		return
	}
	windowsLoading = true
	go p.refreshWindows()
}

// refreshWindows 在背景把窗口表抓回來。逾時放寬（沒有人在等它），失敗就下次再試。
func (p *ClaudeProvider) refreshWindows() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := p.ListModels(ctx)

	windowsMu.Lock()
	defer windowsMu.Unlock()
	windowsLoading = false
	windowsAt = time.Now() // 失敗也記時間：別每建一次引擎就重試一次網路
	if err != nil {
		if windows == nil {
			windows = map[string]int{} // 「查過了、沒拿到」——之後走保守值
		}
		log.Printf("[provider] 問不到模型窗口（暫用保守值 %d，稍後重試）: %v", windowFallback, err)
		return
	}
	m := map[string]int{}
	for _, info := range got {
		m[info.ID] = info.Window
		// 使用者可能設 claude-haiku-4-5，而官方 id 是 claude-haiku-4-5-20251001
		// （實測：舊模型的真實 id 本來就帶日期），兩種寫法都要查得到。
		if k := datedID.ReplaceAllString(info.ID, ""); k != info.ID {
			m[k] = info.Window
		}
	}
	windows = m
	log.Printf("[provider] 模型窗口已更新（%d 個）", len(got))
}

// windowOf 查這個模型的真實輸入窗口；查不到回 0。
//
// 資料就在 /v1/models 的回應裡（max_input_tokens），先前被丟掉了——於是 1M 窗口的模型
// 被當成 200k，壓縮在【實際容量的 15%】就觸發：白丟上下文、還付錢做不必要的摘要。
// 【絕不阻塞】：這支被 NewAgentEngine 同步呼叫，每個任務都會走到。實測首次抓取要好幾秒
// （TLS＋分頁），擋在這裡等於每次任務起手都卡住。所以改成：資料舊了就【背景】去抓，
// 當下先回手上有的（沒有就 0 → 呼叫端用保守值）。第一個任務可能用到保守值，
// 那只是壓縮早一點，不會壞事；第二個任務起就是準的。
func (p *ClaudeProvider) windowOf(model string) int {
	windowsMu.Lock()
	defer windowsMu.Unlock()
	if !windowsLoading && (windows == nil || time.Since(windowsAt) > modelsTTL) {
		windowsLoading = true
		go p.refreshWindows()
	}
	if w, ok := windows[model]; ok {
		return w
	}
	return windows[datedID.ReplaceAllString(model, "")]
}

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
