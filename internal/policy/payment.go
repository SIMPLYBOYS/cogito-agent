package policy

// 支付授權層：把既有的 Deny > Ask > Allow 從「危險動作」延伸到「錢」。
//
// 【為何錢需要自己一層而不是沿用 Rule】工具規則裁決的是「這個呼叫像不像危險」，靠正則比對參數字串。
// 支付要裁決的是【確切條款】——付給誰、多少、哪條鏈、為了哪個任務、什麼時候過期。正則看不出
// 「這筆 0.05 加上今天已花的 9.98 會不會爆掉 10 塊的預算」，那要狀態。
//
// 【為何不是通用的花錢許可】x402 工作坊 §4.4：「簽的是確切參數，不是通用的花錢許可」。
// 所以 intent 是六欄位的請購單，每一欄都是獨立的確定性檢查，任何一欄不合就有明確的拒絕理由——
// 這也是 audit 要落帳的東西（連被拒的那筆，§5.3「被拒絕的 intent 保持可觀測」）。
//
// 【為何 task binding 只有 harness 做得到】錢包與卡層知道「花多少」，不知道「為了哪個 task、
// 在 run-tree 的哪一步」。那是這層存在的理由。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Intent 是一張請購單——agent 能提出的【唯一】支付請求形式。
//
// 它刻意【不含金鑰或簽名】：agent 提單，簽名由出納（policy 後面的簽名服務）做。
// OWASP ASI03「被入侵的 agent 超支」的結構性解法就是這個分離——agent 就算被完全接管，
// 它能做的也只是提一張會被裁決的單。
type Intent struct {
	TaskID    string    `json:"task_id"`    // 綁哪個任務。空＝無主支出，一律 Deny
	Resource  string    `json:"resource"`   // 買什麼（402 challenge 來的路徑）
	Merchant  string    `json:"merchant"`   // 付給誰
	MaxAmount string    `json:"max_amount"` // 上限，十進位字串（"0.05"）。exact＝就是這個數
	Asset     string    `json:"asset"`      // USDC…
	Network   string    `json:"network"`    // base-sepolia…
	ExpiresAt time.Time `json:"expires_at"` // 報價效期
	Scheme    string    `json:"scheme"`     // exact｜upto，空＝exact
	Nonce     string    `json:"nonce"`      // 擋 replay；空＝不做 replay 檢查
}

// PaymentConfig 是 policy.json 的 "payment" 區塊。
//
// 三段門檻（AutoBelow／AskBelow）而不是一條上限：例外管理才是真實的內控形狀——
// 人不看每一筆，人只被例外打斷。小額靜默放行、中額找人、超過就擋。
type PaymentConfig struct {
	Merchants []string `json:"merchants"` // 商家白名單。空＝不限（demo 請務必填，空白名單等於沒有這道檢查）
	Assets    []string `json:"assets"`
	Networks  []string `json:"networks"`

	AutoBelow string `json:"auto_below"` // 單筆低於此值：Allow
	AskBelow  string `json:"ask_below"`  // 單筆低於此值：Ask（超過即 Deny）
	Budget    string `json:"budget"`     // 視窗內累計上限
	WindowSec int    `json:"window_sec"` // 預算與速率的視窗長度，秒（0＝24 小時）
	Velocity  int    `json:"velocity"`   // 視窗內最多幾筆（0＝不限）
}

// Decision 是一次支付裁決的完整結果——audit 要落的就是這個（Deny 也落）。
type Decision struct {
	Action Action `json:"action"`
	Reason string `json:"reason"`
	Rule   string `json:"rule"`   // 哪條檢查說了算，給稽核回放用
	Amount int64  `json:"amount"` // 解析後的微美元（1 USD = 1_000_000），避免浮點誤差
}

// 金額一律用微美元整數運算。浮點在錢的路徑上是不能接受的：0.1+0.2 != 0.3 這種誤差
// 累積在預算比較上，會變成「明明超了卻放行」——而那正是這層存在要擋的事。
const micro = 1_000_000

// parseUSD 解析十進位美元字串成微美元。拒絕負數與空值——兩者都不是合法金額。
func parseUSD(s string) (int64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return 0, fmt.Errorf("金額是空的")
	}
	neg := strings.HasPrefix(s, "-")
	if neg {
		return 0, fmt.Errorf("金額不得為負：%q", s)
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	var v int64
	for _, r := range whole {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("金額格式錯誤：%q", s)
		}
		v = v*10 + int64(r-'0')
		if v > 1<<40 { // 遠超任何合理的 agent 支出，多半是解析到了別的東西
			return 0, fmt.Errorf("金額過大：%q", s)
		}
	}
	// 小數補滿六位再截斷：多的位數直接丟掉，不四捨五入——寧可少算自己的額度，
	// 也不要因為進位讓一筆剛好卡在門檻上的支出被放行。
	if len(frac) > 6 {
		frac = frac[:6]
	}
	for len(frac) < 6 {
		frac += "0"
	}
	var f int64
	for _, r := range frac {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("金額格式錯誤：%q", s)
		}
		f = f*10 + int64(r-'0')
	}
	return v*micro + f, nil
}

// USD 把微美元格式化回可讀字串（audit 與畫面共用同一個表示）。
func USD(v int64) string {
	return fmt.Sprintf("%d.%06d", v/micro, v%micro)
}

// spend 是一筆已成立的支出（用來算預算與速率）。
type spend struct {
	at     time.Time
	amount int64
}

// PaymentGate 是支付裁決器：設定 ＋ 已花帳（預算／速率／replay 都要狀態）。
//
// 帳本【刻意內建】而不是等 audit 層：沒有它，預算與 replay 兩條檢查就是空的，
// 而那兩條正是 demo 的裁決矩陣裡最有說服力的部分。audit 層之後接上來只是把它落盤。
type PaymentGate struct {
	cfg    PaymentConfig
	auto   int64
	ask    int64
	budget int64
	window time.Duration

	spends []spend
	nonces map[string]time.Time
	now    func() time.Time // 測試可換；預設 time.Now
}

// NewPaymentGate 從設定建裁決器。門檻解析不過就回錯——**不靜默降級**：
// 一個解析失敗的上限會變成 0，而 0 在比較裡等於「什麼都超過」或「什麼都不超過」，
// 兩種都是把保護悄悄關掉。寧可開不起來。
func NewPaymentGate(cfg PaymentConfig) (*PaymentGate, error) {
	g := &PaymentGate{cfg: cfg, nonces: map[string]time.Time{}, now: time.Now}
	var err error
	for _, f := range []struct {
		name string
		src  string
		dst  *int64
	}{
		{"auto_below", cfg.AutoBelow, &g.auto},
		{"ask_below", cfg.AskBelow, &g.ask},
		{"budget", cfg.Budget, &g.budget},
	} {
		if strings.TrimSpace(f.src) == "" {
			continue
		}
		if *f.dst, err = parseUSD(f.src); err != nil {
			return nil, fmt.Errorf("policy.json payment.%s: %w", f.name, err)
		}
	}
	if g.ask > 0 && g.auto > g.ask {
		return nil, fmt.Errorf("policy.json payment: auto_below（%s）不該大於 ask_below（%s）",
			USD(g.auto), USD(g.ask))
	}
	g.window = time.Duration(cfg.WindowSec) * time.Second
	if g.window <= 0 {
		g.window = 24 * time.Hour
	}
	return g, nil
}

// LoadPayment 從 policy.json 讀 payment 區塊。沒有這個區塊回 (nil, nil)＝這個部署不開支付閘。
func LoadPayment(data []byte) (*PaymentGate, error) {
	var doc struct {
		Payment *PaymentConfig `json:"payment"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 policy.json 的 payment 區塊失敗: %w", err)
	}
	if doc.Payment == nil {
		return nil, nil
	}
	return NewPaymentGate(*doc.Payment)
}

// Decide 對一張請購單裁決。純函式性質：**不改變帳本**——記帳要等真的付了才算（見 Commit）。
//
// 裁決結果不受檢查順序影響（跟 Rule 一樣用 rank 合併：Deny > Ask > Allow）。
// 但【同級】時回報的是先評估到的那條：順序固定為 task → merchant → asset → network →
// expiry → replay → velocity → budget → cap，由結構性到金額性。所以一張同時踩到
// 商家與預算的單，audit 上寫的是商家——先講「這筆根本不該存在」比先講「太貴」有用。
//
// runningTask 是這個 agent 現在真正在跑的任務 id。空字串＝沒有任務在跑，
// 此時任何支付都是無主的，一律 Deny——這就是 task binding：錢包層做不到的那條。
func (g *PaymentGate) Decide(in Intent, runningTask string) Decision {
	if g == nil {
		return Decision{Action: Deny, Reason: "沒有支付政策——未設定就不放行任何支出", Rule: "no-policy"}
	}
	amount, err := parseUSD(in.MaxAmount)
	if err != nil {
		return Decision{Action: Deny, Reason: "金額無法解析：" + err.Error(), Rule: "amount"}
	}
	d := Decision{Amount: amount}
	worse := func(a Action, rule, reason string) {
		if a.rank() > d.Action.rank() {
			d.Action, d.Rule, d.Reason = a, rule, reason
		}
	}

	// ① task binding——只有 harness 知道這筆錢是為了哪一步花的
	switch {
	case strings.TrimSpace(in.TaskID) == "":
		worse(Deny, "task", "請購單沒有綁任務——無主支出不放行")
	case runningTask != "" && in.TaskID != runningTask:
		worse(Deny, "task", fmt.Sprintf("請購單綁的任務（%s）不是現在在跑的那個（%s）", in.TaskID, runningTask))
	case runningTask == "":
		worse(Deny, "task", "現在沒有任務在跑，這筆支出無主")
	}

	// ② 商家白名單。空白名單＝這道檢查沒開，要講出來而不是靜靜放行
	if len(g.cfg.Merchants) > 0 && !contains(g.cfg.Merchants, in.Merchant) {
		worse(Deny, "merchant", fmt.Sprintf("商家 %q 不在白名單上", in.Merchant))
	}

	// ③ 幣別與鏈
	if len(g.cfg.Assets) > 0 && !contains(g.cfg.Assets, in.Asset) {
		worse(Deny, "asset", fmt.Sprintf("幣別 %q 不在允許清單", in.Asset))
	}
	if len(g.cfg.Networks) > 0 && !contains(g.cfg.Networks, in.Network) {
		worse(Deny, "network", fmt.Sprintf("網路 %q 不在允許清單", in.Network))
	}

	// ④ 效期。報價過期還付＝付一個早就不成立的價格
	if !in.ExpiresAt.IsZero() && !g.now().Before(in.ExpiresAt) {
		worse(Deny, "expiry", "報價已過期（"+in.ExpiresAt.Format(time.RFC3339)+"）")
	}

	// ⑤ replay。同一張單付兩次就是重複扣款——講者 §5.4 的 nonce 表
	if in.Nonce != "" {
		if _, seen := g.nonces[in.Nonce]; seen {
			worse(Deny, "replay", "這張請購單已經用過（nonce 重複）——逾時是未知結果，不是再付一次的許可")
		}
	}

	// ⑥ 速率與預算：視窗內的累計。要在單筆門檻【之前】判，因為它們比單筆更硬
	used, count := g.usedLocked()
	if g.cfg.Velocity > 0 && count >= g.cfg.Velocity {
		worse(Deny, "velocity", fmt.Sprintf("視窗內已有 %d 筆，達速率上限 %d", count, g.cfg.Velocity))
	}
	if g.budget > 0 && used+amount > g.budget {
		worse(Deny, "budget", fmt.Sprintf("這筆 $%s 加上已花的 $%s 會超過預算 $%s",
			USD(amount), USD(used), USD(g.budget)))
	}

	// ⑦ 單筆門檻——三段式例外管理：小額靜默、中額找人、超過擋掉
	switch {
	case g.ask > 0 && amount >= g.ask:
		worse(Deny, "cap", fmt.Sprintf("單筆 $%s 超過上限 $%s", USD(amount), USD(g.ask)))
	case g.auto > 0 && amount >= g.auto:
		worse(Ask, "cap", fmt.Sprintf("單筆 $%s 超過免審額度 $%s，要人核准", USD(amount), USD(g.auto)))
	}

	if d.Action == "" {
		// 沒有任何檢查有意見。但【未設定任何門檻】不等於放行——那是漏設定，不是政策。
		if g.auto == 0 && g.ask == 0 && g.budget == 0 {
			return Decision{Action: Ask, Rule: "unconfigured", Amount: amount,
				Reason: "payment 區塊沒有設任何金額門檻——不當作無限額度，一律找人"}
		}
		d.Action, d.Rule, d.Reason = Allow, "auto", fmt.Sprintf("單筆 $%s 在免審額度內", USD(amount))
	}
	return d
}

// Commit 記一筆【真的付出去】的支出，並封存 nonce。
//
// 刻意與 Decide 分開：裁決 Allow 不等於錢已經離開——中間還有簽名與結算，可能失敗。
// 在 Decide 就記帳會讓失敗的嘗試吃掉預算；在結算成功才記，帳才對得起「實付多少」。
func (g *PaymentGate) Commit(in Intent, amount int64) {
	if g == nil {
		return
	}
	now := g.now()
	g.spends = append(g.spends, spend{at: now, amount: amount})
	if in.Nonce != "" {
		g.nonces[in.Nonce] = now
	}
}

// Used 回視窗內已花金額與筆數（給預算表與 audit 用）。
func (g *PaymentGate) Used() (int64, int) {
	if g == nil {
		return 0, 0
	}
	return g.usedLocked()
}

// Budget 回預算上限（0＝未設）。
func (g *PaymentGate) Budget() int64 {
	if g == nil {
		return 0
	}
	return g.budget
}

func (g *PaymentGate) usedLocked() (int64, int) {
	cut := g.now().Add(-g.window)
	var sum int64
	var n int
	for _, s := range g.spends {
		if s.at.After(cut) {
			sum += s.amount
			n++
		}
	}
	return sum, n
}

// contains 比對時忽略大小寫與前後空白：商家與鏈名在 header 裡大小寫不一定一致，
// 但白名單比對不該因此漏掉——漏掉的方向是「該擋的沒擋」。
func contains(list []string, v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, s := range list {
		if strings.ToLower(strings.TrimSpace(s)) == v {
			return true
		}
	}
	return false
}
