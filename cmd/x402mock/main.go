// x402mock：本地 402 資源伺服器 ＋ facilitator。整圈離線跑 demo 用。
//
//	go run ./cmd/x402mock -addr :4021 -price 0.05
//
// 任何 GET 路徑都是付費資源（GET /premium-data 先拿 402 報價，帶 PAYMENT-SIGNATURE 再來就給資料）。
// /verify、/settle 是 facilitator 端點。-delay 讓結算故意慢，演「逾時是未知結果，不是再付一次的許可」。
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/policy"
	"github.com/SIMPLYBOYS/cogito-agent/internal/x402"
)

func main() {
	addr := flag.String("addr", ":4021", "監聽位址")
	price := flag.String("price", "0.05", "每次請求價格（USD）")
	payTo := flag.String("payto", "0xMOCK-TREASURY", "收款方")
	network := flag.String("network", "eip155:84532", "CAIP-2 網路（預設 Base Sepolia）")
	asset := flag.String("asset", "USDC", "資產")
	secret := flag.String("secret", os.Getenv("X402_MOCK_SECRET"), "HMAC 秘密（與 signer 共用；空＝demo）")
	timeout := flag.Duration("timeout", 60*time.Second, "報價效期")
	delay := flag.Duration("delay", 0, "結算故意慢多少（demo 逾時用）")
	flag.Parse()

	atomic, err := policy.ParseUSDAtomic(*price)
	if err != nil {
		log.Fatalf("價格解析失敗：%v", err)
	}
	if *secret == "" {
		*secret = "demo"
	}
	m := x402.NewMock(x402.MockConfig{
		PriceAtomic: atomic, PayTo: *payTo, Network: *network, Asset: *asset,
		Secret: []byte(*secret), Timeout: *timeout, SettleDelay: *delay,
	})
	log.Printf("[x402mock] 監聯 %s：每次 $%s %s → %s（%s）；facilitator /verify /settle", *addr, *price, *asset, *payTo, *network)
	log.Fatal(http.ListenAndServe(*addr, m.Handler()))
}
