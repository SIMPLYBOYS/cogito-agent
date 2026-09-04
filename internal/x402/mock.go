package x402

// 本地 402 資源伺服器 ＋ facilitator mock：讓整圈離線跑得起來。
//
// 【為何要有它】demo 不該依賴別人的服務（網路 blocker 沒修、真結算不當依賴）。wire 只是三個 header
// ＋ base64 JSON，自己寫比拉 SDK 穩。它 mock 的是【軌道】（結算），不是【控制層】——後者在 policy。
//
// 【結算路徑照工作坊 §5.4】VALIDATE（version／scheme／network）→ MATCH（amount／asset／payTo）→
// RESERVE（nonce）→ SETTLE。nonce 是 server 在 402 時發的：同一張報價只能結一次，重放直接擋。
// 「Timeout 是未知結果，不是再付一次的許可」——所以 RESERVE 之後就算 client 逾時沒收到回應，
// 這個 nonce 也已經用掉了，第二次來會被當 replay 擋下，而不是再扣一次。
//
// 【簽名】mock 用 HMAC-SHA256（shared secret）代替 EIP-712。結構上等價：簽的是 Authorization 的
// 確切欄位；驗的是簽名對不對、簽的內容跟報價一不一致。接真鏈時換 Signer 與 verify 就好。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MockConfig 是 mock 伺服器的參數。
type MockConfig struct {
	PriceAtomic string        // 每次請求的價格，atomic units（"50000" ＝ $0.05）
	PayTo       string        // 收款方
	Network     string        // CAIP-2
	Asset       string        // "USDC"
	Secret      []byte        // HMAC 秘密：facilitator 用它驗簽；Signer 用它簽
	Timeout     time.Duration // 報價效期（maxTimeoutSeconds）
	SettleDelay time.Duration // 結算故意慢多少（demo「逾時不重付」用）；0＝不慢
}

// nonceState 是 RESERVE 表的一格。
type nonceState struct {
	issued  time.Time
	expires time.Time
	settled bool
	tx      string
}

// Mock 同時是資源伺服器與 facilitator。
type Mock struct {
	cfg    MockConfig
	mu     sync.Mutex
	nonces map[string]*nonceState
}

// NewMock 建 mock。
func NewMock(cfg MockConfig) *Mock {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Mock{cfg: cfg, nonces: map[string]*nonceState{}}
}

// Handler 回 http.Handler：任何 GET 都是付費資源；/verify 與 /settle 是 facilitator 端點。
func (m *Mock) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/verify", m.handleVerify)
	mux.HandleFunc("/settle", m.handleSettle)
	mux.HandleFunc("/", m.handleResource)
	return mux
}

// SettledCount 回已結算筆數（測試與 demo 面板用）。
func (m *Mock) SettledCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.nonces {
		if s.settled {
			n++
		}
	}
	return n
}

// ── 資源端 ──────────────────────────────────────────────────────────────────

func (m *Mock) handleResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	sig := r.Header.Get(HeaderSignature)
	if sig == "" {
		m.challenge(w, r, "")
		return
	}
	var pp PaymentPayload
	if err := Decode(sig, &pp); err != nil {
		m.challenge(w, r, "PAYMENT-SIGNATURE 解不開："+err.Error())
		return
	}
	res := m.verifyAndSettle(pp, r.URL.Path)
	enc, _ := Encode(res)
	w.Header().Set(HeaderResponse, enc)
	if !res.Success {
		// 結算失敗仍回 402：client 看得到 SettleResponse 的理由，決定要不要重新報價
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": res.ErrorReason})
		return
	}
	// 付了就給資源。內容是假的研究資料——demo 裡 agent 真的會拿它繼續做事
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource": r.URL.Path,
		"data":     fmt.Sprintf("premium dataset for %s（paid %s atomic %s）", r.URL.Path, pp.Payload.Authorization.Value, m.cfg.Asset),
		"tx":       res.Transaction,
	})
}

// challenge 發 402：報價裡帶一個新 nonce，記進 RESERVE 表（未結算狀態）。
func (m *Mock) challenge(w http.ResponseWriter, r *http.Request, why string) {
	nonce := newNonce()
	now := time.Now()
	m.mu.Lock()
	m.nonces[nonce] = &nonceState{issued: now, expires: now.Add(m.cfg.Timeout)}
	m.mu.Unlock()
	req := PaymentRequired{
		X402Version: Version,
		Error:       why,
		Accepts: []Accept{{
			Scheme:            SchemeExact,
			Network:           m.cfg.Network,
			MaxAmountRequired: m.cfg.PriceAtomic,
			Resource:          r.URL.Path,
			Description:       "premium research data",
			MimeType:          "application/json",
			PayTo:             m.cfg.PayTo,
			MaxTimeoutSeconds: int(m.cfg.Timeout / time.Second),
			Asset:             m.cfg.Asset,
			Extra:             map[string]any{"nonce": nonce, "expiresAt": now.Add(m.cfg.Timeout).Format(time.RFC3339)},
		}},
	}
	enc, _ := Encode(req)
	w.Header().Set(HeaderRequired, enc)
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "payment required"})
}

// ── facilitator 端 ──────────────────────────────────────────────────────────

// Verify 是唯讀檢查（工作坊 §3.2：verify 不改狀態）。回空字串＝通過。
func (m *Mock) Verify(pp PaymentPayload, resource string) string {
	// VALIDATE
	if pp.X402Version != Version {
		return fmt.Sprintf("version %d 不支援", pp.X402Version)
	}
	if pp.Scheme != SchemeExact && pp.Scheme != SchemeUpto {
		return "scheme 不支援：" + pp.Scheme
	}
	if pp.Network != m.cfg.Network {
		return fmt.Sprintf("network 不符：要 %s，來 %s", m.cfg.Network, pp.Network)
	}
	// MATCH：簽的內容必須跟報價一致（防報價替換）
	a := pp.Payload.Authorization
	if a.To != m.cfg.PayTo {
		return "收款方不符"
	}
	if pp.Accepted.Asset != m.cfg.Asset {
		return "資產不符"
	}
	if resource != "" && pp.Accepted.Resource != resource {
		return "資源不符：簽的不是這個路徑"
	}
	switch pp.Scheme {
	case SchemeExact:
		if a.Value != m.cfg.PriceAtomic {
			return fmt.Sprintf("金額不符：要 %s，簽 %s", m.cfg.PriceAtomic, a.Value)
		}
	case SchemeUpto:
		// upto：授權上限 ≥ 價格即可，實付＝價格
		if cmpAtomic(a.Value, m.cfg.PriceAtomic) < 0 {
			return fmt.Sprintf("授權上限 %s 低於價格 %s", a.Value, m.cfg.PriceAtomic)
		}
	}
	now := time.Now().Unix()
	if a.ValidAfter > now || (a.ValidBefore > 0 && a.ValidBefore < now) {
		return "授權不在有效區間"
	}
	// nonce 要是我發的、沒過期、沒用過（用過的在 settle 那步擋，這裡只看存在與效期）
	m.mu.Lock()
	st, ok := m.nonces[a.Nonce]
	m.mu.Unlock()
	if !ok {
		return "nonce 不是本伺服器發的"
	}
	if time.Now().After(st.expires) {
		return "報價已過期"
	}
	// 簽名
	if !m.checkSig(a, pp.Payload.Signature) {
		return "簽名無效"
	}
	return ""
}

// verifyAndSettle：VALIDATE→MATCH（Verify）→ RESERVE → SETTLE。
func (m *Mock) verifyAndSettle(pp PaymentPayload, resource string) SettleResponse {
	if why := m.Verify(pp, resource); why != "" {
		return SettleResponse{Success: false, ErrorReason: why, Network: m.cfg.Network}
	}
	nonce := pp.Payload.Authorization.Nonce
	// RESERVE：原子地把 nonce 標成已用。第二個拿同一個 nonce 來的，在這裡被擋——
	// 不管第一個之後有沒有成功回到 client 手上。這就是「逾時不重付」的實作。
	m.mu.Lock()
	st := m.nonces[nonce]
	if st.settled {
		m.mu.Unlock()
		return SettleResponse{Success: false, ErrorReason: "replay：這張報價已經結算過", Network: m.cfg.Network}
	}
	st.settled = true
	st.tx = "0xmock" + hex.EncodeToString(sha256.New().Sum([]byte(nonce)))[:24]
	m.mu.Unlock()
	// SETTLE：故意慢（demo 逾時用）——注意這是在 RESERVE 之後，所以就算 client 等不到，帳已經定了
	if m.cfg.SettleDelay > 0 {
		time.Sleep(m.cfg.SettleDelay)
	}
	return SettleResponse{Success: true, Transaction: st.tx, Network: m.cfg.Network,
		Payer: pp.Payload.Authorization.From}
}

type facilitatorReq struct {
	PaymentPayload PaymentPayload `json:"paymentPayload"`
	Resource       string         `json:"resource,omitempty"`
}

func (m *Mock) handleVerify(w http.ResponseWriter, r *http.Request) {
	var in facilitatorReq
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	why := m.Verify(in.PaymentPayload, in.Resource)
	_ = json.NewEncoder(w).Encode(map[string]any{"isValid": why == "", "invalidReason": why})
}

func (m *Mock) handleSettle(w http.ResponseWriter, r *http.Request) {
	var in facilitatorReq
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(m.verifyAndSettle(in.PaymentPayload, in.Resource))
}

// ── 簽名（mock：HMAC 代替 EIP-712） ─────────────────────────────────────────

// SignMessage 是簽名的訊息本體：Authorization 的確切欄位。簽名綁死這些欄位，改任何一個都驗不過。
func SignMessage(a Authorization) []byte {
	return []byte(strings.Join([]string{a.From, a.To, a.Value,
		fmt.Sprint(a.ValidAfter), fmt.Sprint(a.ValidBefore), a.Nonce}, "|"))
}

// Sign 用 secret 對 Authorization 簽名（mock）。
func Sign(secret []byte, a Authorization) string {
	h := hmac.New(sha256.New, secret)
	h.Write(SignMessage(a))
	return hex.EncodeToString(h.Sum(nil))
}

func (m *Mock) checkSig(a Authorization, sig string) bool {
	want := Sign(m.cfg.Secret, a)
	return hmac.Equal([]byte(want), []byte(sig))
}

func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// cmpAtomic 比較兩個十進位整數字串（atomic units 不會有小數）。回 -1／0／1。
func cmpAtomic(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}
