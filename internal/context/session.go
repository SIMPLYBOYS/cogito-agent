package context

import (
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
	mu      sync.RWMutex

	// 該 Session 累計消耗的資源（由外部 CostTracker 通過 RecordUsage 累加）
	TotalPromptTokens     int
	TotalCompletionTokens int
	TotalCostUSD          float64
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

// Append 是長期記憶的寫入口（thinking / action / observation 都落這裡）。
func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()
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

// SessionManager 併發安全地按 ID（如 Slack channelID）管理 session 池。
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
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
	sess := NewSession(id, workDir)
	sm.sessions[id] = sess
	return sess
}
