package payment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/policy"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
	"github.com/SIMPLYBOYS/cogito-agent/internal/x402"
)

// 一套完整的離線環境：402 mock ＋ 政策 ＋ 帳本 ＋ 出納。
type rig struct {
	srv    *httptest.Server
	mock   *x402.Mock
	gate   *policy.PaymentGate
	ledger *policy.Ledger
	tool   *RequestPaymentTool
	asked  []policy.Intent // approver 被問了什麼
}

func newRig(t *testing.T, approve func(policy.Intent, policy.Decision) (bool, string, string), price string, delay time.Duration) *rig {
	t.Helper()
	r := &rig{}
	r.mock = x402.NewMock(x402.MockConfig{PriceAtomic: price, PayTo: "0xTREASURY", Network: "eip155:84532",
		Asset: "USDC", Secret: []byte("s"), Timeout: time.Minute, SettleDelay: delay})
	r.srv = httptest.NewServer(r.mock.Handler())
	t.Cleanup(r.srv.Close)
	host := strings.TrimPrefix(r.srv.URL, "http://")
	var err error
	r.gate, err = policy.NewPaymentGate(policy.PaymentConfig{
		Merchants: []string{host}, Assets: []string{"USDC"}, Networks: []string{"eip155:84532"},
		AutoBelow: "0.10", AskBelow: "5.00", Budget: "10.00", WindowSec: 3600, Velocity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r.ledger, err = policy.NewLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	var appr PaymentApprover
	if approve != nil {
		appr = func(_ context.Context, in policy.Intent, d policy.Decision) (bool, string, string) {
			r.asked = append(r.asked, in)
			return approve(in, d)
		}
	}
	r.tool = NewRequestPaymentTool(r.gate, r.ledger, appr, HMACSigner{Secret: []byte("s"), Addr: "0xAGENT"})
	return r
}

func taskCtx(task string) context.Context {
	return tools.WithTask(context.Background(), tools.TaskContext{AgentID: "office:p19", TaskID: task})
}

func args(url string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"url": url, "purpose": "研究任務要這份資料"})
	return b
}

func (r *rig) entries(t *testing.T) []policy.AuditEntry {
	t.Helper()
	es, err := r.ledger.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// TestAllowSettlesAndCommits：小額自動放行 → 簽 → 結算 → 記帳 → 帳本一筆 settled、Approver=policy。
func TestAllowSettlesAndCommits(t *testing.T) {
	r := newRig(t, nil, "50000", 0)
	out, err := r.tool.Execute(taskCtx("T1"), args(r.srv.URL+"/premium-data"))
	if err != nil {
		t.Fatalf("該成功: %v", err)
	}
	if !strings.Contains(out, "已結算") || !strings.Contains(out, "0xmock") {
		t.Fatalf("回執要有 tx: %s", out)
	}
	if used, n := r.gate.Used(); used != 50_000 || n != 1 {
		t.Fatalf("結算後要 Commit 進預算，實得 $%s / %d", policy.USD(used), n)
	}
	es := r.entries(t)
	if len(es) != 1 || !es[0].Settled || es[0].Approver != "policy" || es[0].TxRef == "" {
		t.Fatalf("帳本要有一筆 settled、policy 放行、帶 tx: %+v", es)
	}
	if es[0].TaskID != "T1" || es[0].Agent != "office:p19" {
		t.Fatalf("帳本要記誰、為了哪個任務: %+v", es[0])
	}
}

// TestDenyIsPolicyDenied：商家不在白名單 → ErrPolicyDenied（引擎終止該目標，不教它繞）；
// 帳本【也要有這筆】——被拒的意圖保持可觀測。
func TestDenyIsPolicyDenied(t *testing.T) {
	r := newRig(t, nil, "50000", 0)
	// 另開一個「陌生商家」：同一個 mock 邏輯、不同 host
	evil := httptest.NewServer(r.mock.Handler())
	defer evil.Close()
	_, err := r.tool.Execute(taskCtx("T1"), args(evil.URL+"/premium-data"))
	if err == nil || !strings.Contains(err.Error(), tools.ErrPolicyDenied.Error()) {
		t.Fatalf("要走 ErrPolicyDenied，實得 %v", err)
	}
	if r.mock.SettledCount() != 0 {
		t.Fatal("Deny 不該碰到結算")
	}
	es := r.entries(t)
	if len(es) != 1 || es[0].Decision.Action != policy.Deny || es[0].Decision.Rule != "merchant" || es[0].Settled {
		t.Fatalf("Deny 也要落帳，且理由是商家: %+v", es)
	}
	if es[0].Approver != "" {
		t.Fatal("被拒的沒有放行者")
	}
}

// TestAskThenHumanApproves：中額 → 找人 → 人說好 → 結算；帳本 Approver=human:<id>。
func TestAskThenHumanApproves(t *testing.T) {
	r := newRig(t, func(policy.Intent, policy.Decision) (bool, string, string) { return true, "boss", "" }, "1000000", 0)
	out, err := r.tool.Execute(taskCtx("T1"), args(r.srv.URL+"/premium-data"))
	if err != nil {
		t.Fatalf("該成功: %v", err)
	}
	if len(r.asked) != 1 || r.asked[0].MaxAmount != "1.000000" {
		t.Fatalf("要問人一次、帶正確金額: %+v", r.asked)
	}
	if !strings.Contains(out, "已結算") {
		t.Fatal("核准後要結算")
	}
	es := r.entries(t)
	if len(es) != 1 || es[0].Approver != "human:boss" || !es[0].Settled {
		t.Fatalf("帳本要記是人放行的: %+v", es)
	}
}

// TestAskThenHumanRejects：人說不 → 不結算、不記預算、【不標 Denied】（人在現場，理由可引導）。
func TestAskThenHumanRejects(t *testing.T) {
	r := newRig(t, func(policy.Intent, policy.Decision) (bool, string, string) {
		return false, "boss", "太貴，找免費來源"
	}, "1000000", 0)
	_, err := r.tool.Execute(taskCtx("T1"), args(r.srv.URL+"/premium-data"))
	if err == nil || !strings.Contains(err.Error(), "太貴") {
		t.Fatalf("要把人的理由帶給 agent: %v", err)
	}
	if strings.Contains(err.Error(), tools.ErrPolicyDenied.Error()) {
		t.Fatal("人拒絕不該標成政策 Deny——那會讓引擎終止目標而不是換路")
	}
	if r.mock.SettledCount() != 0 {
		t.Fatal("人拒絕不該結算")
	}
	if used, _ := r.gate.Used(); used != 0 {
		t.Fatal("沒付出去的不該吃預算")
	}
	es := r.entries(t)
	if len(es) != 1 || es[0].Approver != "" || !strings.Contains(es[0].Error, "人拒絕") {
		t.Fatalf("帳本要記人拒絕: %+v", es)
	}
}

// TestAskUnattendedDenies：需要人但沒有審批管道 → fail-closed Deny。
func TestAskUnattendedDenies(t *testing.T) {
	r := newRig(t, nil, "1000000", 0)
	_, err := r.tool.Execute(taskCtx("T1"), args(r.srv.URL+"/premium-data"))
	if err == nil || !strings.Contains(err.Error(), tools.ErrPolicyDenied.Error()) {
		t.Fatalf("無人值守要 Deny: %v", err)
	}
}

// TestNoTaskIsDenied：ctx 沒有 TaskID ＝ 無主支出，一律 Deny。task binding 不由 agent 的參數決定。
func TestNoTaskIsDenied(t *testing.T) {
	r := newRig(t, nil, "50000", 0)
	_, err := r.tool.Execute(context.Background(), args(r.srv.URL+"/premium-data"))
	if err == nil || !strings.Contains(err.Error(), "無主") {
		t.Fatalf("沒綁任務要 Deny 且講明無主: %v", err)
	}
	es := r.entries(t)
	if len(es) != 1 || es[0].Decision.Rule != "task" {
		t.Fatalf("帳本要記是 task 那條擋的: %+v", es)
	}
}

// TestSettleTimeoutIsUnknownNotRetry：結算逾時 → 錯誤明講「不要再提一次」、不 Commit、帳本記未知。
// 「Timeout 是未知結果，不是再付一次的許可」。
func TestSettleTimeoutIsUnknownNotRetry(t *testing.T) {
	r := newRig(t, nil, "50000", 400*time.Millisecond)
	r.tool.client = &http.Client{Timeout: 100 * time.Millisecond}
	_, err := r.tool.Execute(taskCtx("T1"), args(r.srv.URL+"/premium-data"))
	if err == nil || !strings.Contains(err.Error(), "不要") || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("逾時要明講別重提、會被當 replay: %v", err)
	}
	if used, _ := r.gate.Used(); used != 0 {
		t.Fatal("結果未知的不該 Commit 進預算——那是對帳的事，不是猜的事")
	}
	es := r.entries(t)
	if len(es) != 1 || es[0].Settled || !strings.Contains(es[0].Error, "未知") {
		t.Fatalf("帳本要記結算未知: %+v", es)
	}
}

// TestFreeResourcePassthrough：不收費的資源直接給內容，不走請購。
func TestFreeResourcePassthrough(t *testing.T) {
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("free data"))
	}))
	defer free.Close()
	r := newRig(t, nil, "50000", 0)
	out, err := r.tool.Execute(taskCtx("T1"), args(free.URL+"/x"))
	if err != nil || !strings.Contains(out, "free data") {
		t.Fatalf("免費資源該直接給: %v %s", err, out)
	}
	if len(r.entries(t)) != 0 {
		t.Fatal("沒有支付就沒有帳")
	}
}

// TestLedgerIsAppendOnlyJSONL：帳本是一行一筆的 JSONL，落在 <root>/.claw/audit/payments.jsonl。
func TestLedgerIsAppendOnlyJSONL(t *testing.T) {
	root := t.TempDir()
	l, err := policy.NewLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Append(policy.AuditEntry{Agent: "a", TaskID: "T1"})
	_ = l.Append(policy.AuditEntry{Agent: "b", TaskID: "T2"})
	b, err := os.ReadFile(filepath.Join(root, ".claw", "audit", "payments.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; lines != 2 {
		t.Fatalf("要兩行，實得 %d：%s", lines, b)
	}
	es, _ := l.ReadAll()
	if len(es) != 2 || es[1].TaskID != "T2" {
		t.Fatalf("讀回要照順序: %+v", es)
	}
}
