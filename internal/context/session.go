package context

import (
	"log"
	"sync"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// Session 把"對話歷史 + 工作目錄"提升為一級實體。history 私有 + RWMutex 保護，
// 避免外部直接持有 slice 引用造成 data race。同一個 AgentEngine 可服務多個不同
// WorkDir 的 Session（workspace 跟著 session 走，不跟著 engine 走）。
type Session struct {
	ID        string
	WorkDir   string
	CreatedAt time.Time
	UpdatedAt time.Time

	history []schema.Message
	// summary 是「已逐出 history 的早期訊息」的滾動摘要（salience-aware）。搭配末 N 條逐字，
	// 讓 history 有界（[摘要] + 末 N 逐字）又不失早期脈絡。由引擎的 summarizer 維護。
	summary string
	// planMode 是本會話（頻道）是否走 Plan Mode——per-channel 切換（`plan on`/`plan off`），
	// 預設關。開了則該頻道之後的任務外部化計畫到 PLAN.md/TODO.md，並啟用目標錨與確定性步驟跳過。
	planMode bool
	mu       sync.RWMutex

	// 該 Session 累計消耗的資源（由外部 CostTracker 通過 RecordUsage 累加）
	TotalPromptTokens     int
	TotalCompletionTokens int
	TotalCostUSD          float64

	// store 非 nil 時開啟 write-through 持久化（每次變動落盤）。nil 則純記憶體（預設）。
	store SessionStore
}

func NewSession(id string, workDir string) *Session {
	return &Session{
		ID:        id,
		WorkDir:   workDir,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		history:   make([]schema.Message, 0),
	}
}

// newSessionFromSnapshot 從持久化快照復原 Session，並綁回 store 以便後續繼續 write-through。
func newSessionFromSnapshot(snap *SessionSnapshot, store SessionStore) *Session {
	created, _ := time.Parse(time.RFC3339Nano, snap.CreatedAt)
	updated, _ := time.Parse(time.RFC3339Nano, snap.UpdatedAt)
	return &Session{
		ID:                    snap.ID,
		WorkDir:               snap.WorkDir,
		CreatedAt:             created,
		UpdatedAt:             updated,
		history:               snap.History,
		summary:               snap.Summary,
		planMode:              snap.PlanMode,
		TotalPromptTokens:     snap.TotalPromptTokens,
		TotalCompletionTokens: snap.TotalCompletionTokens,
		TotalCostUSD:          snap.TotalCostUSD,
		store:                 store,
	}
}

// snapshotLocked 在持有鎖時打一份可序列化快照（history 做拷貝，避免外部持有底層 slice）。
func (s *Session) snapshotLocked() *SessionSnapshot {
	h := make([]schema.Message, len(s.history))
	copy(h, s.history)
	return &SessionSnapshot{
		ID:                    s.ID,
		WorkDir:               s.WorkDir,
		CreatedAt:             s.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:             s.UpdatedAt.Format(time.RFC3339Nano),
		History:               h,
		Summary:               s.summary,
		PlanMode:              s.planMode,
		TotalPromptTokens:     s.TotalPromptTokens,
		TotalCompletionTokens: s.TotalCompletionTokens,
		TotalCostUSD:          s.TotalCostUSD,
	}
}

// persistLocked 在【持有鎖時】落盤。刻意在鎖內寫：Append/RecordUsage 是序列化的，鎖內寫能保證
// 落盤順序與變動順序一致，杜絕「舊快照覆蓋新快照」；單 session 檔案小、原子寫，鎖內 I/O 成本可忽略。
func (s *Session) persistLocked() {
	if s.store == nil {
		return
	}
	if err := s.store.Save(s.snapshotLocked()); err != nil {
		log.Printf("[Session] 持久化失敗 (%s): %v", s.ID, err)
	}
}

// Append 是長期記憶的寫入口（thinking / action / observation 都落這裡）。
func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()
	s.persistLocked()
}

// GetWorkingMemory 返回短期工作記憶：末尾 limit 條的滑動窗口。
// 關鍵防禦：若窗口首條是 ToolResult（RoleUser + ToolCallID），說明它對應的
// assistant tool_use 已被截斷在窗口外——把"無主的 tool_result"發給 LLM API 會報錯，
// 因此從頭部一路剝掉孤兒，直到窗口首條是合法的 turn 起點。
func (s *Session) GetWorkingMemory(limit int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.history)
	if total <= limit || limit <= 0 {
		res := make([]schema.Message, total)
		copy(res, s.history)
		return res
	}

	res := make([]schema.Message, limit)
	copy(res, s.history[total-limit:])

	for len(res) > 0 {
		if res[0].Role == schema.RoleUser && res[0].ToolCallID != "" {
			res = res[1:]
		} else {
			break
		}
	}

	return res
}

// Summary 回傳目前的滾動摘要（已逐出 history 的早期訊息之 salience 壓縮）。
func (s *Session) Summary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summary
}

// PlanMode 回傳本會話是否走 Plan Mode（per-channel 切換）。
func (s *Session) PlanMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.planMode
}

// SetPlanMode 切換本會話的 Plan Mode 並落盤（`plan on`/`plan off` 指令用）。
func (s *Session) SetPlanMode(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.planMode = on
	s.UpdatedAt = time.Now()
	s.persistLocked()
}

// EvictablePrefix 回傳「超出逐字尾、應摺進摘要」的前綴【拷貝】：history 條數未達 trigger 時回 nil
// （多數短對話不觸發、零成本）。刻意不變動 history——交給 CommitSummary 在摘要成功後才真正丟棄，
// 確保摘要失敗不丟資料。
func (s *Session) EvictablePrefix(keepTail, trigger int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := len(s.history)
	if total <= trigger || total <= keepTail {
		return nil
	}
	n := total - keepTail
	out := make([]schema.Message, n)
	copy(out, s.history[:n])
	return out
}

// CommitSummary 在摘要成功後原子生效：設定新摘要 + 從 history 頭部真正丟棄 dropN 條，使 history
// 有界（[摘要] + 末 N 逐字）。dropN 來自先前 EvictablePrefix 的長度；Append 只加尾部、前綴穩定，
// 故按數量丟棄安全。重建 slice（非 reslice）讓舊底層陣列可被 GC 回收，記憶體真正收斂。
func (s *Session) CommitSummary(newSummary string, dropN int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dropN > len(s.history) {
		dropN = len(s.history)
	}
	remaining := make([]schema.Message, len(s.history)-dropN)
	copy(remaining, s.history[dropN:])
	s.history = remaining
	s.summary = newSummary
	s.UpdatedAt = time.Now()
	s.persistLocked()
}

// SessionManager 併發安全地按 ID（如 Slack channelID）管理 session 池。
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
	store    SessionStore // 非 nil 時，GetOrCreate 會從磁碟復原、新 session 也綁定 write-through
}

// SetStore 開啟持久化：之後 GetOrCreate 會優先從 store 復原、新建的 session 也會 write-through。
func (sm *SessionManager) SetStore(store SessionStore) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.store = store
}

// GlobalSessionMgr 是包級全局單例，方便各 IM adapter（Slack 等）共享同一 session 池。
var GlobalSessionMgr = &SessionManager{
	sessions: make(map[string]*Session),
}

// RecordUsage 供外部 CostTracker 調用，累加本 Session 的 Token 與費用賬單。
func (s *Session) RecordUsage(prompt int, completion int, cost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalPromptTokens += prompt
	s.TotalCompletionTokens += completion
	s.TotalCostUSD += cost
	s.persistLocked()
}

// CostUSD 在鎖保護下快照當前累計花費，供引擎做成本熔斷判斷時併發安全地讀取
// （避免與 RecordUsage 的寫入直接競爭裸欄位 TotalCostUSD）。
func (s *Session) CostUSD() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TotalCostUSD
}

func (sm *SessionManager) GetOrCreate(id string, workDir string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sess, exists := sm.sessions[id]; exists {
		return sess
	}

	// 記憶體沒有 → 嘗試從持久化後端復原（跨重啟續傳的關鍵）。
	if sm.store != nil {
		if snap, found, err := sm.store.Load(id); err != nil {
			log.Printf("[Session] 載入 (%s) 失敗，改新建: %v", id, err)
		} else if found {
			sess := newSessionFromSnapshot(snap, sm.store)
			if workDir != "" {
				sess.WorkDir = workDir // 以當前傳入的工作區為準（重啟後路徑可能變），但保留歷史
			}
			sm.sessions[id] = sess
			log.Printf("[Session] 從磁碟復原 (%s)，歷史 %d 則、累計 $%.6f", id, len(sess.history), sess.TotalCostUSD)
			return sess
		}
	}

	sess := NewSession(id, workDir)
	sess.store = sm.store // 綁定 store（可能為 nil）→ 開啟 write-through
	sm.sessions[id] = sess
	return sess
}
