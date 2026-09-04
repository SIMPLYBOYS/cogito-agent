package policy

// 金流稽核帳：每一張請購單的裁決都落一筆——【被拒的也落】。
//
// 工作坊 §5.3：「只有通過驗證的路徑能觸及 custody；被拒絕的 intent 保持可觀測（供稽核與攻擊偵測）」。
// facilitator 只看得到成交的那些；一個被 prompt injection 帶偏、試圖付給陌生商家的 agent，
// 它留下的唯一證據就是這裡的 Deny 記錄。不落 Deny 的帳本，是半套。
//
// 格式是 append-only JSONL：一行一筆、只追加不改寫。稽核要的是「當時發生了什麼」，
// 不是「現在的狀態」——可改寫的檔案講不出前者。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry 是一筆稽核記錄：誰提／哪條規則／誰批／簽了什麼／結算成什麼。
type AuditEntry struct {
	At       time.Time `json:"at"`
	Agent    string    `json:"agent"`   // 誰提的（office:p19）
	TaskID   string    `json:"task_id"` // 為了哪個任務
	Intent   Intent    `json:"intent"`
	Decision Decision  `json:"decision"`
	// Approver：這筆最終是誰放行的。"policy" ＝ 政策自動放行；"human:<id>" ＝ 人核准；
	// "" ＝ 沒走到放行（Deny／人拒絕／逾時）。跟 Decision.Action 分開記：Ask 之後人說不，
	// Action 仍是 ask，但 Approver 是空的——兩個欄位合起來才是完整的故事。
	Approver string `json:"approver,omitempty"`
	Settled  bool   `json:"settled"`
	TxRef    string `json:"tx_ref,omitempty"`
	Error    string `json:"error,omitempty"` // 結算失敗／逾時未知／人拒絕的理由
}

// Ledger 是帳本檔的寫入端。並發安全：多個 agent 同時付款不能把兩行寫成一行。
type Ledger struct {
	path string
	mu   sync.Mutex
}

// LedgerPath 回帳本位置：<root>/.claw/audit/payments.jsonl。
func LedgerPath(root string) string {
	return filepath.Join(root, ".claw", "audit", "payments.jsonl")
}

// NewLedger 建帳本（目錄不存在就建）。
func NewLedger(root string) (*Ledger, error) {
	p := LedgerPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, fmt.Errorf("建 audit 目錄失敗: %w", err)
	}
	return &Ledger{path: p}, nil
}

// Append 追加一筆。寫失敗回錯但【不擋支付流程】由呼叫端決定——通常記 log 就好：
// 帳本壞了不該讓一筆已經核准的付款卡住，但也不能靜默，所以錯誤要往上傳。
func (l *Ledger) Append(e AuditEntry) error {
	if l == nil {
		return nil
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ReadAll 讀整本帳（給測試與面板用；正式量大時該改分頁，但一場 demo 不會）。
// 壞掉的行跳過不擋——一行寫壞不該讓整本帳讀不出來。
func (l *Ledger) ReadAll() ([]AuditEntry, error) {
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}
