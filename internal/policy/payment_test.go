package policy

import (
	"testing"
	"time"
)

// 一組固定的測試設定：小額 $0.10 以下自動、$5 以下找人、超過擋掉；
// 視窗內預算 $10、最多 5 筆。
func testGate(t *testing.T) *PaymentGate {
	t.Helper()
	g, err := NewPaymentGate(PaymentConfig{
		Merchants: []string{"localhost:4021"},
		Assets:    []string{"USDC"},
		Networks:  []string{"base-sepolia"},
		AutoBelow: "0.10",
		AskBelow:  "5.00",
		Budget:    "10.00",
		WindowSec: 3600,
		Velocity:  5,
	})
	if err != nil {
		t.Fatalf("建 gate 失敗: %v", err)
	}
	return g
}

func okIntent() Intent {
	return Intent{
		TaskID: "T1", Resource: "/premium-data", Merchant: "localhost:4021",
		MaxAmount: "0.05", Asset: "USDC", Network: "base-sepolia",
		ExpiresAt: time.Now().Add(time.Minute), Scheme: "exact", Nonce: "n1",
	}
}

// TestPaymentMatrix 是裁決矩陣本體：Allow／Ask／Deny × 預算／商家／過期／replay。
// 評審追問「如果 X 呢」的答案全在這張表裡。
func TestPaymentMatrix(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Intent)
		want Action
		rule string
	}{
		{"小額自動放行", func(i *Intent) {}, Allow, "auto"},
		{"中額要人核准", func(i *Intent) { i.MaxAmount = "1.00" }, Ask, "cap"},
		{"超過單筆上限擋掉", func(i *Intent) { i.MaxAmount = "9.99" }, Deny, "cap"},
		{"商家不在白名單", func(i *Intent) { i.Merchant = "evil.example" }, Deny, "merchant"},
		{"幣別不允許", func(i *Intent) { i.Asset = "DOGE" }, Deny, "asset"},
		{"鏈不允許", func(i *Intent) { i.Network = "mainnet" }, Deny, "network"},
		{"報價過期", func(i *Intent) { i.ExpiresAt = time.Now().Add(-time.Second) }, Deny, "expiry"},
		{"沒綁任務", func(i *Intent) { i.TaskID = "" }, Deny, "task"},
		{"綁到別的任務", func(i *Intent) { i.TaskID = "T9" }, Deny, "task"},
		{"金額爛掉", func(i *Intent) { i.MaxAmount = "abc" }, Deny, "amount"},
		{"金額為負", func(i *Intent) { i.MaxAmount = "-1.00" }, Deny, "amount"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := testGate(t)
			in := okIntent()
			c.mut(&in)
			d := g.Decide(in, "T1")
			if d.Action != c.want || d.Rule != c.rule {
				t.Fatalf("要 %s/%s，實得 %s/%s（%s）", c.want, c.rule, d.Action, d.Rule, d.Reason)
			}
			if d.Reason == "" {
				t.Fatal("裁決一定要附理由——audit 要落帳的就是這個，Deny 也不例外")
			}
		})
	}
}

// TestDenyBeatsAllow 釘住「順序不影響安全」：同一張單同時踩到 Allow 級與 Deny 級的檢查，
// 結果必須是 Deny。這是既有 Action.rank 的性質，支付層沿用它而不是自己排序。
func TestDenyBeatsAllow(t *testing.T) {
	g := testGate(t)
	in := okIntent()
	in.MaxAmount = "0.01"        // 金額本身小到會 Allow
	in.Merchant = "evil.example" // 但商家不合
	if d := g.Decide(in, "T1"); d.Action != Deny {
		t.Fatalf("金額小不該蓋過商家不合，實得 %s（%s）", d.Action, d.Reason)
	}
}

// TestReplay：同一個 nonce 付第二次要擋。
// 講者的話是「Timeout 是未知結果，不是再付一次的許可」——重試在支付情境就是重複扣款。
func TestReplay(t *testing.T) {
	g := testGate(t)
	in := okIntent()
	first := g.Decide(in, "T1")
	if first.Action != Allow {
		t.Fatalf("第一次該放行，實得 %s", first.Action)
	}
	g.Commit(in, first.Amount)
	if d := g.Decide(in, "T1"); d.Action != Deny || d.Rule != "replay" {
		t.Fatalf("同一張單再付一次要擋，實得 %s/%s", d.Action, d.Rule)
	}
}

// TestBudget：單筆都在額度內，累計超過預算就要擋。
// 這條是錢包層做不到的——它看得到單筆，看不到「這個視窗內這個 task 已經花了多少」。
func TestBudget(t *testing.T) {
	g := testGate(t)
	// 只放兩筆：要隔離出【預算】這條。放到速率上限（5 筆）的話會先被 velocity 擋下，
	// 測到的就不是預算了（踩過：原本放 5 筆，紅字寫的是 velocity）。
	for i := 0; i < 2; i++ {
		in := okIntent()
		in.MaxAmount = "0.09" // 每筆都在免審額度內
		in.Nonce = string(rune('a' + i))
		d := g.Decide(in, "T1")
		if d.Action != Allow {
			t.Fatalf("第 %d 筆該放行，實得 %s（%s）", i+1, d.Action, d.Reason)
		}
		g.Commit(in, d.Amount)
	}
	// 人為把預算拉到快滿，再試一筆（總共 3 筆，仍在速率上限內）
	g.spends = append(g.spends, spend{at: time.Now(), amount: 9_800_000})
	in := okIntent()
	in.MaxAmount = "0.09"
	in.Nonce = "last"
	if d := g.Decide(in, "T1"); d.Action != Deny || d.Rule != "budget" {
		t.Fatalf("累計超預算要擋，實得 %s/%s（%s）", d.Action, d.Rule, d.Reason)
	}
}

// TestVelocity：視窗內筆數上限。
func TestVelocity(t *testing.T) {
	g := testGate(t)
	for i := 0; i < 5; i++ {
		g.spends = append(g.spends, spend{at: time.Now(), amount: 1})
	}
	in := okIntent()
	if d := g.Decide(in, "T1"); d.Action != Deny || d.Rule != "velocity" {
		t.Fatalf("達速率上限要擋，實得 %s/%s", d.Action, d.Rule)
	}
}

// TestCommitOnlyOnSettle：裁決成功【不】記帳——中間還有簽名與結算會失敗。
// 在 Decide 就記帳的話，失敗的嘗試會吃掉預算，帳就對不起「實付多少」。
func TestCommitOnlyOnSettle(t *testing.T) {
	g := testGate(t)
	in := okIntent()
	g.Decide(in, "T1")
	if used, n := g.Used(); used != 0 || n != 0 {
		t.Fatalf("只裁決不該記帳，實得 $%s / %d 筆", USD(used), n)
	}
	g.Commit(in, 50_000)
	if used, n := g.Used(); used != 50_000 || n != 1 {
		t.Fatalf("結算後要記帳，實得 $%s / %d 筆", USD(used), n)
	}
}

// TestWindowExpires：視窗外的舊支出不該再佔預算。
func TestWindowExpires(t *testing.T) {
	g := testGate(t)
	g.spends = append(g.spends, spend{at: time.Now().Add(-2 * time.Hour), amount: 9_000_000})
	if used, n := g.Used(); used != 0 || n != 0 {
		t.Fatalf("視窗外的支出不該計入，實得 $%s / %d 筆", USD(used), n)
	}
}

// TestUnconfiguredIsAsk：沒設任何門檻【不等於】無限額度。漏設定要往安全的方向倒。
func TestUnconfiguredIsAsk(t *testing.T) {
	g, err := NewPaymentGate(PaymentConfig{Merchants: []string{"localhost:4021"},
		Assets: []string{"USDC"}, Networks: []string{"base-sepolia"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := g.Decide(okIntent(), "T1"); d.Action != Ask || d.Rule != "unconfigured" {
		t.Fatalf("沒設門檻要一律找人，實得 %s/%s", d.Action, d.Rule)
	}
}

// TestNilGateDenies：沒有支付政策時不放行任何支出（fail-closed）。
func TestNilGateDenies(t *testing.T) {
	var g *PaymentGate
	if d := g.Decide(okIntent(), "T1"); d.Action != Deny {
		t.Fatalf("沒有政策要 fail-closed，實得 %s", d.Action)
	}
}

// TestBadConfigFailsLoud：門檻解析不過要開不起來，不是靜默變 0。
// 解析失敗變 0 的話，上限就等於「沒有上限」——保護被悄悄關掉。
func TestBadConfigFailsLoud(t *testing.T) {
	if _, err := NewPaymentGate(PaymentConfig{AskBelow: "五塊"}); err == nil {
		t.Fatal("爛掉的金額設定要回錯")
	}
	if _, err := NewPaymentGate(PaymentConfig{AutoBelow: "5.00", AskBelow: "1.00"}); err == nil {
		t.Fatal("auto_below 大於 ask_below 是矛盾設定，要回錯")
	}
}

// TestParseUSD 釘住金額解析——這是錢的路徑，浮點誤差不能接受。
func TestParseUSD(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
	}{
		{"0.05", 50_000}, {"1", 1_000_000}, {"$2.50", 2_500_000},
		{"0.000001", 1}, {"10.000000", 10_000_000},
		{"0.1234567", 123_456}, // 第七位直接截掉，不進位——寧可少算自己的額度
	} {
		got, err := parseUSD(c.in)
		if err != nil || got != c.want {
			t.Fatalf("parseUSD(%q)＝%d,%v，要 %d", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "-1", "1.2.3", "1e5"} {
		if _, err := parseUSD(bad); err == nil {
			t.Fatalf("parseUSD(%q) 該回錯", bad)
		}
	}
}

// TestLoadPayment：policy.json 沒有 payment 區塊＝這個部署不開支付閘（不是錯誤）。
func TestLoadPayment(t *testing.T) {
	g, err := LoadPayment([]byte(`{"rules":[]}`))
	if err != nil || g != nil {
		t.Fatalf("沒有 payment 區塊該回 (nil,nil)，實得 %v,%v", g, err)
	}
	g, err = LoadPayment([]byte(`{"payment":{"auto_below":"0.10","ask_below":"5.00"}}`))
	if err != nil || g == nil {
		t.Fatalf("有 payment 區塊該建起來，實得 %v,%v", g, err)
	}
	if g.auto != 100_000 || g.ask != 5_000_000 {
		t.Fatalf("門檻解析錯：auto=%d ask=%d", g.auto, g.ask)
	}
}
