package x402

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestMock(t *testing.T, delay time.Duration) (*Mock, *httptest.Server) {
	t.Helper()
	m := NewMock(MockConfig{PriceAtomic: "50000", PayTo: "0xTREASURY", Network: "eip155:84532",
		Asset: "USDC", Secret: []byte("s"), Timeout: time.Minute, SettleDelay: delay})
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(srv.Close)
	return m, srv
}

// 拿一張報價，順手解出 nonce。
func challenge(t *testing.T, srv *httptest.Server) Accept {
	t.Helper()
	resp, err := http.Get(srv.URL + "/premium-data")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("沒付錢要回 402，實得 %d", resp.StatusCode)
	}
	var pr PaymentRequired
	if err := Decode(resp.Header.Get(HeaderRequired), &pr); err != nil {
		t.Fatalf("PAYMENT-REQUIRED 解不開: %v", err)
	}
	if pr.X402Version != Version || len(pr.Accepts) == 0 {
		t.Fatalf("報價形狀不對: %+v", pr)
	}
	return pr.Accepts[0]
}

// 照報價簽一張合法的 payload。
func signFor(acc Accept, secret string, mut func(*Authorization)) string {
	a := Authorization{From: "0xAGENT", To: acc.PayTo, Value: acc.MaxAmountRequired,
		ValidAfter: time.Now().Unix() - 5, ValidBefore: time.Now().Unix() + 60, Nonce: acc.Extra["nonce"].(string)}
	if mut != nil {
		mut(&a)
	}
	pp := PaymentPayload{X402Version: Version, Scheme: acc.Scheme, Network: acc.Network, Accepted: acc}
	pp.Payload.Authorization = a
	pp.Payload.Signature = Sign([]byte(secret), a)
	enc, _ := Encode(pp)
	return enc
}

func pay(t *testing.T, srv *httptest.Server, path, sig string) (*http.Response, SettleResponse) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set(HeaderSignature, sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var sr SettleResponse
	if h := resp.Header.Get(HeaderResponse); h != "" {
		_ = Decode(h, &sr)
	}
	return resp, sr
}

// TestHappyPath：402 → 簽 → 200 ＋ PAYMENT-RESPONSE 帶 tx。整圈離線。
func TestHappyPath(t *testing.T) {
	m, srv := newTestMock(t, 0)
	acc := challenge(t, srv)
	resp, sr := pay(t, srv, "/premium-data", signFor(acc, "s", nil))
	if resp.StatusCode != http.StatusOK || !sr.Success || sr.Transaction == "" {
		t.Fatalf("要 200＋成功＋tx，實得 %d %+v", resp.StatusCode, sr)
	}
	if m.SettledCount() != 1 {
		t.Fatalf("該結算 1 筆，實得 %d", m.SettledCount())
	}
}

// TestReplayBlocked：同一張報價（同 nonce）付第二次要被 RESERVE 表擋下。
// 這是「Timeout 是未知結果，不是再付一次的許可」的伺服器端保證。
func TestReplayBlocked(t *testing.T) {
	m, srv := newTestMock(t, 0)
	acc := challenge(t, srv)
	sig := signFor(acc, "s", nil)
	pay(t, srv, "/premium-data", sig)
	resp, sr := pay(t, srv, "/premium-data", sig)
	if resp.StatusCode == http.StatusOK || sr.Success {
		t.Fatal("重放同一個 nonce 不該再結算一次")
	}
	if m.SettledCount() != 1 {
		t.Fatalf("重放後仍只該有 1 筆，實得 %d", m.SettledCount())
	}
}

// TestVerifyMatrix：MATCH 那幾條——金額／收款方／網路／資源／簽名，任一不合都不結算。
func TestVerifyMatrix(t *testing.T) {
	cases := []struct {
		name string
		sig  func(Accept) string
		path string
	}{
		{"金額改小", func(a Accept) string { return signFor(a, "s", func(x *Authorization) { x.Value = "1" }) }, "/premium-data"},
		{"收款方換人", func(a Accept) string { return signFor(a, "s", func(x *Authorization) { x.To = "0xEVIL" }) }, "/premium-data"},
		{"簽名用錯 secret", func(a Accept) string { return signFor(a, "wrong", nil) }, "/premium-data"},
		{"nonce 不是伺服器發的", func(a Accept) string { return signFor(a, "s", func(x *Authorization) { x.Nonce = "made-up" }) }, "/premium-data"},
		{"簽的資源路徑不對", func(a Accept) string { return signFor(a, "s", nil) }, "/other-data"},
		{"授權已過期", func(a Accept) string {
			return signFor(a, "s", func(x *Authorization) { x.ValidBefore = time.Now().Unix() - 1 })
		}, "/premium-data"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, srv := newTestMock(t, 0)
			acc := challenge(t, srv)
			resp, sr := pay(t, srv, c.path, c.sig(acc))
			if resp.StatusCode == http.StatusOK || sr.Success {
				t.Fatalf("不該結算，實得 %d %+v", resp.StatusCode, sr)
			}
			if sr.ErrorReason == "" {
				t.Fatal("拒絕要附理由——client 要知道是報價換了還是簽錯了")
			}
			if m.SettledCount() != 0 {
				t.Fatal("驗不過的不該動到帳")
			}
		})
	}
}

// TestUptoAcceptsHigherCap：upto 的授權上限 ≥ 價格即可；低於價格擋下。
func TestUptoAcceptsHigherCap(t *testing.T) {
	_, srv := newTestMock(t, 0)
	acc := challenge(t, srv)
	acc.Scheme = SchemeUpto
	ok := signFor(acc, "s", func(x *Authorization) { x.Value = "70000" })
	if resp, sr := pay(t, srv, "/premium-data", ok); resp.StatusCode != http.StatusOK || !sr.Success {
		t.Fatalf("upto 上限高於價格該過，實得 %d %+v", resp.StatusCode, sr)
	}
	acc2 := challenge(t, srv)
	acc2.Scheme = SchemeUpto
	low := signFor(acc2, "s", func(x *Authorization) { x.Value = "40000" })
	if resp, sr := pay(t, srv, "/premium-data", low); resp.StatusCode == http.StatusOK || sr.Success {
		t.Fatal("upto 上限低於價格不該過")
	}
}

// TestVerifyIsReadOnly：facilitator 的 /verify 不改狀態——驗完還能結算；/settle 才 RESERVE。
func TestVerifyIsReadOnly(t *testing.T) {
	m, srv := newTestMock(t, 0)
	acc := challenge(t, srv)
	var pp PaymentPayload
	_ = Decode(signFor(acc, "s", nil), &pp)
	if why := m.Verify(pp, "/premium-data"); why != "" {
		t.Fatalf("合法 payload 該驗過，實得 %q", why)
	}
	if why := m.Verify(pp, "/premium-data"); why != "" {
		t.Fatalf("verify 兩次都該過（唯讀），實得 %q", why)
	}
	if m.SettledCount() != 0 {
		t.Fatal("verify 不該結算")
	}
	if sr := m.verifyAndSettle(pp, "/premium-data"); !sr.Success {
		t.Fatalf("settle 該成功: %+v", sr)
	}
	if sr := m.verifyAndSettle(pp, "/premium-data"); sr.Success {
		t.Fatal("同一 nonce 第二次 settle 要擋")
	}
}

// TestReserveBeforeSlowSettle：結算故意慢時，nonce 在【慢之前】就已 RESERVE——
// client 逾時放棄後再來一次，會被當 replay 擋，不會重扣。
func TestReserveBeforeSlowSettle(t *testing.T) {
	m, srv := newTestMock(t, 300*time.Millisecond)
	acc := challenge(t, srv)
	sig := signFor(acc, "s", nil)
	fast := &http.Client{Timeout: 50 * time.Millisecond}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/premium-data", nil)
	req.Header.Set(HeaderSignature, sig)
	if _, err := fast.Do(req); err == nil {
		t.Fatal("前置條件：這次應該逾時")
	}
	time.Sleep(350 * time.Millisecond) // 讓慢結算跑完
	resp, sr := pay(t, srv, "/premium-data", sig)
	if resp.StatusCode == http.StatusOK || sr.Success {
		t.Fatal("逾時後重送同一張單，要被當 replay 擋下（不重扣）")
	}
	if m.SettledCount() != 1 {
		t.Fatalf("帳上只該有那一筆已 RESERVE 的，實得 %d", m.SettledCount())
	}
}
