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

// Accept 是報價裡的一個可付選項（工作坊 §2.4）。Client 從中選一個「它能簽、facilitator 能結算」的。
type Accept struct {
	Scheme            string         `json:"scheme"`
	Network           string         `json:"network"`           // CAIP-2，如 eip155:84532（Base Sepolia）
	MaxAmountRequired string         `json:"maxAmountRequired"` // atomic units，十進位字串
	Resource          string         `json:"resource"`
	Description       string         `json:"description,omitempty"`
	MimeType          string         `json:"mimeType,omitempty"`
	PayTo             string         `json:"payTo"`
	MaxTimeoutSeconds int            `json:"maxTimeoutSeconds"`
	Asset             string         `json:"asset"`
	Extra             map[string]any `json:"extra,omitempty"` // mock 用它帶 nonce／expiresAt
}

// PaymentRequired 是 402 回應的 header 內容。
type PaymentRequired struct {
	X402Version int      `json:"x402Version"`
	Error       string   `json:"error,omitempty"`
	Accepts     []Accept `json:"accepts"`
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
	X402Version int    `json:"x402Version"`
	Scheme      string `json:"scheme"`
	Network     string `json:"network"`
	Accepted    Accept `json:"accepted"`
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
