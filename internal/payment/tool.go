// Package payment 是請購單工具——agent 花錢的唯一入口。獨立成套件是因為 policy 已 import tools
// （guard 中介層），工具若放進 tools 就成環；這裡同時依賴兩邊，兩邊都不必回頭認識它。
package payment

// request_payment：agent 花錢的【唯一】入口——而且它不是付款，是【提請購單】。
//
// 工作坊 §4.1 五角色的第一行：「AI Agent：只提議購買」。這個工具的名字、回傳值、與它碰不到的東西
// 都是安全設計的一部分：
//   - 名字叫 request，不叫 pay：agent 拿到的是裁決結果與回執，錢有沒有出去在它視野之外。
//   - 它拿不到 signer：簽名由 policy 後面的 Signer 做（辦公室裡＝出納）。agent 就算被完全接管，
//     能做的也只是提一張會被裁決的單（OWASP ASI03）。
//   - task binding 不由 agent 填：TaskID 從 ctx 來（harness 設的），agent 的參數裡沒有這一欄——
//     否則被帶偏的 agent 可以自己宣稱「這是為了 T1」。
//
// 八步驟（工作坊 §6）：REQUEST → 402 CHALLENGE → BIND INTENT → POLICY → SIGN → RETRY → VERIFY+SETTLE → COMPLETE。
// 每一步失敗都有明確的、給 agent 看的理由；Deny 走 tools.ErrPolicyDenied（引擎終止該目標而不是教它繞）。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/policy"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
	"github.com/SIMPLYBOYS/cogito-agent/internal/x402"
)

// PaymentApprover 是 Ask 時去問人的回呼（辦公室＝審批卡投到老闆房門口）。
// 回 (approved, 誰批的, 理由)。誰批的要記進帳——「人核准」跟「政策自動放行」是兩種不同的證據。
type PaymentApprover func(ctx context.Context, intent policy.Intent, d policy.Decision) (approved bool, by string, reason string)

// Signer 是出納：只簽已核准的確切條款。agent 拿不到它。
type Signer interface {
	Sign(a x402.Authorization) (signature string, err error)
	From() string // 付款方地址（agent funding wallet）
}

// HMACSigner 是 mock 出納：共用秘密簽 HMAC。接真鏈時換成 EIP-712 signer，介面不變。
type HMACSigner struct {
	Secret []byte
	Addr   string
}

func (s HMACSigner) Sign(a x402.Authorization) (string, error) { return x402.Sign(s.Secret, a), nil }
func (s HMACSigner) From() string                              { return s.Addr }

// RequestPaymentTool 是請購單工具。
type RequestPaymentTool struct {
	gate    *policy.PaymentGate
	ledger  *policy.Ledger
	approve PaymentApprover
	signer  Signer
	client  *http.Client
}

// NewRequestPaymentTool 組工具。gate 為 nil 時所有支付 Deny（fail-closed，見 PaymentGate.Decide）。
func NewRequestPaymentTool(gate *policy.PaymentGate, ledger *policy.Ledger, approve PaymentApprover, signer Signer) *RequestPaymentTool {
	return &RequestPaymentTool{gate: gate, ledger: ledger, approve: approve, signer: signer,
		// 逾時要短：結算等不到就當【未知】，不重試。這個數字是 demo 節奏，不是安全參數
		client: &http.Client{Timeout: 8 * time.Second}}
}

func (t *RequestPaymentTool) Name() string { return "request_payment" }

func (t *RequestPaymentTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "為【目前這個任務】提出一張付費資源的請購單（x402）。你只能提單、不能付款：" +
			"系統會依政策裁決（自動放行／找人核准／拒絕），核准後由出納簽名付款，你拿到的是裁決結果、" +
			"回執與資源內容。被拒絕就是被拒絕——不要換個寫法再試，那是同一張單。" +
			"用途：任務需要的付費資料／API（撞到 HTTP 402 的那些）。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":     map[string]any{"type": "string", "description": "要取得的付費資源網址"},
				"purpose": map[string]any{"type": "string", "description": "一句話：這筆錢是為了任務的哪一步（會出現在請購單上給核准的人看）"},
			},
			"required": []string{"url", "purpose"},
		},
	}
}

// Execute 走完八步驟。回傳給 agent 的文字刻意把「裁決」與「結果」分開講。
func (t *RequestPaymentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL     string `json:"url"`
		Purpose string `json:"purpose"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.URL) == "" {
		return "", fmt.Errorf("需要 url（付費資源網址）與 purpose（用途）")
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url 不是合法的 http(s) 網址")
	}
	task := tools.TaskFromContext(ctx)

	// ① REQUEST → ② 402 CHALLENGE
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("連不上資源：%w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		// 不收費的資源：不該走這個工具，但也不算錯——直接把內容給它
		if resp.StatusCode == http.StatusOK {
			return "（這個資源不收費，直接取得）\n" + schema.TruncRunes(string(body), 4000, "…"), nil
		}
		return "", fmt.Errorf("資源回 %d，不是 402", resp.StatusCode)
	}
	var pr x402.PaymentRequired
	if err := x402.Decode(resp.Header.Get(x402.HeaderRequired), &pr); err != nil {
		return "", fmt.Errorf("402 報價解不開：%w", err)
	}
	acc, ok := pickAccept(pr.Accepts)
	if !ok {
		return "", fmt.Errorf("報價裡沒有我們能簽的選項（scheme 要 exact 或 upto）")
	}

	// ③ BIND INTENT：六欄位請購單。TaskID 從 ctx 來，不從 agent 的參數來
	intent := BindIntent(pr, acc, in.URL, u.Host, task.TaskID)

	// ④ POLICY
	d := t.gate.Decide(intent, task.TaskID)
	entry := policy.AuditEntry{Agent: task.AgentID, TaskID: task.TaskID, Intent: intent, Decision: d}
	card := renderCard(intent, in.Purpose, d)

	switch d.Action {
	case policy.Deny:
		t.audit(entry)
		return "", fmt.Errorf("%w：%s\n%s", tools.ErrPolicyDenied, d.Reason, card)
	case policy.Ask:
		if t.approve == nil {
			entry.Error = "需人核准但沒有審批管道（無人值守）"
			t.audit(entry)
			return "", fmt.Errorf("%w：這筆需要人核准，但目前無人值守——自動拒絕。%s", tools.ErrPolicyDenied, d.Reason)
		}
		okAppr, by, why := t.approve(ctx, intent, d)
		if !okAppr {
			entry.Error = "人拒絕：" + why
			t.audit(entry)
			// 人拒絕【不標 Denied】：人在現場，理由可引導 agent 改走別條路（與 Guard 同一道理）
			return "", fmt.Errorf("請購單被駁回：%s\n%s", why, card)
		}
		entry.Approver = "human:" + by
	case policy.Allow:
		entry.Approver = "policy"
	}

	// ⑤ SIGN：出納簽確切條款。agent 到這裡為止都沒碰過 signer
	now := time.Now().Unix()
	auth := x402.Authorization{
		From: t.signer.From(), To: acc.PayTo, Value: acc.AmountAtomic(),
		ValidAfter: now - 5, ValidBefore: intent.ExpiresAt.Unix(), Nonce: intent.Nonce,
	}
	sig, err := t.signer.Sign(auth)
	if err != nil {
		entry.Error = "簽名失敗：" + err.Error()
		t.audit(entry)
		return "", fmt.Errorf("出納簽名失敗：%w", err)
	}
	pp := x402.PaymentPayload{X402Version: x402.Version, Scheme: acc.Scheme, Network: acc.Network, Accepted: acc, Resource: pr.Resource}
	pp.Payload.Signature, pp.Payload.Authorization = sig, auth
	encPP, _ := x402.Encode(pp)

	// ⑥ RETRY → ⑦ VERIFY+SETTLE → ⑧ COMPLETE
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	req2.Header.Set(x402.HeaderSignature, encPP)
	resp2, err := t.client.Do(req2)
	if err != nil {
		// 【逾時是未知結果，不是再付一次的許可】。這筆可能已經結算（RESERVE 在 server 端已發生），
		// 所以不 Commit 也不重試——記成未知，讓人去對帳。agent 收到的訊息明講「別再提一次」。
		entry.Error = "結算結果未知（" + err.Error() + "）——nonce 已用，不得重付"
		t.audit(entry)
		return "", fmt.Errorf("結算結果未知（%v）。這張請購單的 nonce 已經送出，【不要】再提一次同樣的單——重提會被當 replay 擋下，且可能造成重複扣款的對帳問題。等人對帳。", err)
	}
	body2, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	resp2.Body.Close()
	var sr x402.SettleResponse
	if hdr := resp2.Header.Get(x402.HeaderResponse); hdr != "" {
		_ = x402.Decode(hdr, &sr)
	}
	if resp2.StatusCode != http.StatusOK || !sr.Success {
		entry.Error = "結算失敗：" + orStr(sr.ErrorReason, fmt.Sprintf("HTTP %d", resp2.StatusCode))
		t.audit(entry)
		return "", fmt.Errorf("已核准但結算失敗：%s", entry.Error)
	}
	// 只有到這裡才記帳：錢真的離開了
	t.gate.Commit(intent, d.Amount)
	entry.Settled, entry.TxRef = true, sr.Transaction
	t.audit(entry)

	return fmt.Sprintf("%s\n✅ 已結算　tx: %s（%s）\n\n%s", card, sr.Transaction, sr.Network,
		schema.TruncRunes(string(body2), 4000, "…")), nil
}

func (t *RequestPaymentTool) audit(e policy.AuditEntry) {
	if err := t.ledger.Append(e); err != nil {
		log.Printf("[payment] ⚠ 稽核帳寫入失敗（支付流程不受影響，但這筆沒留證據）：%v", err)
	}
}

// BindIntent 把一張 402 報價綁成請購單（純函式，供 live 測試不付款就能驗到這一步）。
//
// 【nonce 由付款方產】EIP-3009 的 nonce 是 payer 選的 32 bytes（真伺服器不會在報價裡給）。
// 第一版照自己 mock 的設計去讀 extra.nonce，對著真伺服器會讀成 "<nil>"。現在：伺服器若真的
// 給了（我們 mock 為了 demo 的 replay 節拍會給）就用它，否則自己產。replay 保護在 policy 的
// nonce 表與 facilitator 的 RESERVE 表兩邊都成立，跟 nonce 是誰產的無關。
//
// 【金額】v2 叫 amount、v1 叫 maxAmountRequired，AmountAtomic 兩者都吃。
// 【資源】v2 在頂層 resource.url；沒有就用請求的 URL。
func BindIntent(pr x402.PaymentRequired, acc x402.Accept, reqURL, merchant, taskID string) policy.Intent {
	resource := reqURL
	if pr.Resource != nil && pr.Resource.URL != "" {
		resource = pr.Resource.URL
	}
	nonce := ""
	if v, ok := acc.Extra["nonce"].(string); ok && v != "" {
		nonce = v
	} else {
		nonce = clientNonce()
	}
	in := policy.Intent{
		TaskID:    taskID,
		Resource:  resource,
		Merchant:  merchant,
		MaxAmount: policy.USDFromAtomic(acc.AmountAtomic()),
		Asset:     acc.Asset,
		Network:   acc.Network,
		Scheme:    acc.Scheme,
		Nonce:     nonce,
		ExpiresAt: time.Now().Add(time.Duration(acc.MaxTimeoutSeconds) * time.Second),
	}
	if exp, ok := acc.Extra["expiresAt"].(string); ok {
		if ts, err := time.Parse(time.RFC3339, exp); err == nil {
			in.ExpiresAt = ts
		}
	}
	return in
}

// clientNonce 產 EIP-3009 形狀的 nonce：0x ＋ 32 bytes hex。
func clientNonce() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return "0x" + hex.EncodeToString(b[:])
}

// pickAccept 選第一個我們能簽的選項。
func pickAccept(as []x402.Accept) (x402.Accept, bool) {
	for _, a := range as {
		if a.Scheme == x402.SchemeExact || a.Scheme == x402.SchemeUpto {
			return a, true
		}
	}
	return x402.Accept{}, false
}

// renderCard 把請購單排成人讀的樣子——審批卡與 agent 看到的是同一份，不會各說各話。
func renderCard(in policy.Intent, purpose string, d policy.Decision) string {
	var b strings.Builder
	b.WriteString("💳 請購單\n")
	fmt.Fprintf(&b, "  任務：%s\n  用途：%s\n  資源：%s\n  商家：%s\n  金額：$%s %s（%s）\n  網路：%s\n  效期：%s\n",
		orStr(in.TaskID, "（無主）"), purpose, in.Resource, in.Merchant, in.MaxAmount, in.Asset, in.Scheme,
		in.Network, in.ExpiresAt.Format("15:04:05"))
	fmt.Fprintf(&b, "  裁決：%s（%s）— %s", strings.ToUpper(string(d.Action)), d.Rule, d.Reason)
	return b.String()
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
