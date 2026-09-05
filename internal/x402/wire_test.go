package x402

import (
	"encoding/base64"
	"os"
	"testing"
)

// 這條釘的是「解得開【真伺服器】吐的 402」，不是「解得開自己寫的 mock」。
// fixture 是 2026-09-06 從 https://test402.com/api/x402 抓下來的 PAYMENT-REQUIRED（x402 v2）。
// 第一版 wire 型別照印象寫成 v1 形狀（maxAmountRequired、resource 在 accept 裡），對這份會把金額讀成空
// ——mock 自己寫、自己一定解得開，正是這種盲點的來源。
func TestDecodesRealV2Quote(t *testing.T) {
	raw, err := os.ReadFile("testdata/test402_payment_required.json")
	if err != nil {
		t.Fatal(err)
	}
	var pr PaymentRequired
	if err := Decode(base64.StdEncoding.EncodeToString(raw), &pr); err != nil {
		t.Fatalf("真報價解不開: %v", err)
	}
	if pr.X402Version != 2 {
		t.Fatalf("version 要 2，實得 %d", pr.X402Version)
	}
	if pr.Resource == nil || pr.Resource.URL != "https://test402.com/api/x402" {
		t.Fatalf("v2 的 resource 在頂層: %+v", pr.Resource)
	}
	if len(pr.Accepts) != 1 {
		t.Fatalf("要 1 個 accept: %+v", pr.Accepts)
	}
	a := pr.Accepts[0]
	if a.AmountAtomic() != "100" {
		t.Fatalf("v2 金額欄叫 amount，要讀到 100，實得 %q（Amount=%q Max=%q）", a.AmountAtomic(), a.Amount, a.MaxAmountRequired)
	}
	if a.Scheme != SchemeExact || a.Network != "eip155:84532" || a.PayTo == "" || a.MaxTimeoutSeconds != 300 {
		t.Fatalf("核心欄位讀錯: %+v", a)
	}
	if a.Asset != "0x036CbD53842c5426634e7929541eC2318f3dCF7e" {
		t.Fatalf("v2 的 asset 是合約地址: %q", a.Asset)
	}
	if a.Extra["name"] != "USDC" || a.Extra["version"] != "2" || a.Extra["assetTransferMethod"] != "eip3009" {
		t.Fatalf("extra 要帶 EIP-712 domain 與轉帳方法: %+v", a.Extra)
	}
	if _, has := a.Extra["nonce"]; has {
		t.Fatal("真伺服器不在報價裡給 nonce——client 要自己產（見 payment.BindIntent）")
	}
	if pr.Extensions == nil {
		t.Fatal("extensions 要能讀（facilitator 位址在裡面）")
	}
}

// TestV1LegacyAmountStillReads：舊格式（maxAmountRequired）當備援仍讀得到——升級不該把舊伺服器踢掉。
func TestV1LegacyAmountStillReads(t *testing.T) {
	a := Accept{MaxAmountRequired: "50000"}
	if a.AmountAtomic() != "50000" {
		t.Fatalf("v1 備援讀不到: %q", a.AmountAtomic())
	}
	a.Amount = "100"
	if a.AmountAtomic() != "100" {
		t.Fatal("v2 amount 要優先於 v1 欄位")
	}
}
