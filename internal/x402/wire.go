// Package x402 是 x402 v2 的 wire 層：三個 header ＋ base64 JSON。
//
// 【刻意只做 wire】x402 標準化的是「支付協商」不是「支付軌道」（工作坊 §2.1）——custody／預算／
// agent policy 全在協議之外。這個套件只負責把報價、簽名授權、結算結果編碼成 header 能載的形狀；
// 誰能簽、簽多少、綁哪個任務，一律是 policy 套件的事。
//
// 【金額單位】accepts 裡的金額是 atomic units（工作坊 §2.4）。USDC 是 6 位小數，所以 atomic 正好
// 等於 policy 套件的微美元（1 USD = 1_000_000）——兩邊不必換算，也就沒有換算誤差可以藏。
// mock 一律假設資產是 6 位小數的美元穩定幣；接真鏈時這個假設要按資產查表。
package x402

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

const (
	Version = 2

	// 三個 header（v2 改名自 v1 的 X-PAYMENT／X-PAYMENT-RESPONSE）。
	HeaderRequired  = "PAYMENT-REQUIRED"  // Server → Agent：機器可讀報價
	HeaderSignature = "PAYMENT-SIGNATURE" // Agent → Server：對單一選定選項的簽名授權
	HeaderResponse  = "PAYMENT-RESPONSE"  // Server → Agent：結算結果與交易參照

	SchemeExact = "exact" // 固定金額
	SchemeUpto  = "upto"  // 上限授權，實際用量 ≤ cap 計費
)

// Resource 是 v2 報價頂層的資源描述（v1 把它塞在每個 accept 裡；v2 抽到頂層）。
type Resource struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// Accept 是報價裡的一個可付選項（工作坊 §2.4）。Client 從中選一個「它能簽、facilitator 能結算」的。
//
// 【欄位名是對著真伺服器量的】2026-09-06 拿 test402.com（x402 v2）的 402 逐鍵比對：
// 每格的金額叫 `amount`，不是 v1 的 `maxAmountRequired`；resource 不在 accept 裡而在頂層；
// extra 帶的是 EIP-712 domain（name／version）與 assetTransferMethod。第一版照印象寫成 v1 形狀，
// 對著真伺服器會把金額讀成空——這正是「mock 是自己寫的，所以自己一定解得開」的盲點。
// fixture 在 testdata/test402_payment_required.json，wire_test 釘住它。
type Accept struct {
	Scheme            string         `json:"scheme"`
	Network           string         `json:"network"`                     // CAIP-2，如 eip155:84532（Base Sepolia）
	Amount            string         `json:"amount,omitempty"`            // v2：atomic units，十進位字串
	MaxAmountRequired string         `json:"maxAmountRequired,omitempty"` // v1 舊名；讀取時當備援
	PayTo             string         `json:"payTo"`
	MaxTimeoutSeconds int            `json:"maxTimeoutSeconds"`
	Asset             string         `json:"asset"` // v2 真伺服器給的是合約地址（0x036C…＝Base Sepolia USDC）
	Extra             map[string]any `json:"extra,omitempty"`
	// 以下三個是 v1 殘留：v2 伺服器不會給。留著 omitempty 只為讀舊格式不炸。
	Resource    string `json:"resource,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// AmountAtomic 回這格的金額（atomic units）：v2 的 amount 優先，沒有才退到 v1 的 maxAmountRequired。
func (a Accept) AmountAtomic() string {
	if a.Amount != "" {
		return a.Amount
	}
	return a.MaxAmountRequired
}

// PaymentRequired 是 402 回應的 header 內容（v2）。
type PaymentRequired struct {
	X402Version int            `json:"x402Version"`
	Error       string         `json:"error,omitempty"`
	Resource    *Resource      `json:"resource,omitempty"`
	Accepts     []Accept       `json:"accepts"`
	Extensions  map[string]any `json:"extensions,omitempty"`
}

// Authorization 對應 EIP-3009 transferWithAuthorization 的欄位（工作坊 §3.1）：
// 一次簽名綁死一筆轉帳——from／to／value／有效區間／nonce。
type Authorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"` // atomic units
	ValidAfter  int64  `json:"validAfter"`
	ValidBefore int64  `json:"validBefore"`
	Nonce       string `json:"nonce"`
}

// PaymentPayload 是 PAYMENT-SIGNATURE 的內容：選定的條款 ＋ 對它的簽名。
// ⚠ Accepted 必須與 server 驗證的 requirement 完全一致（防報價替換，工作坊 §2.5）。
type PaymentPayload struct {
	X402Version int            `json:"x402Version"`
	Scheme      string         `json:"scheme,omitempty"`  // 便利欄位；權威在 Accepted.Scheme
	Network     string         `json:"network,omitempty"` // 同上
	Resource    *Resource      `json:"resource,omitempty"`
	Accepted    Accept         `json:"accepted"`
	Extensions  map[string]any `json:"extensions,omitempty"`
	Payload     struct {
		Signature     string        `json:"signature"`
		Authorization Authorization `json:"authorization"`
	} `json:"payload"`
}

// SettleResponse 是 PAYMENT-RESPONSE 的內容。
type SettleResponse struct {
	Success     bool   `json:"success"`
	ErrorReason string `json:"errorReason,omitempty"`
	Transaction string `json:"transaction,omitempty"`
	Network     string `json:"network,omitempty"`
	Payer       string `json:"payer,omitempty"`
}

// Encode 把任一 wire 物件編成 header 值（JSON → base64）。
func Encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Decode 從 header 值解回 wire 物件。空字串視為錯誤——呼叫端該先判斷 header 在不在。
func Decode(s string, v any) error {
	if s == "" {
		return fmt.Errorf("header 是空的")
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("base64 解碼失敗: %w", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("JSON 解析失敗: %w", err)
	}
	return nil
}
