package context

import (
	"sync"
	"time"

	"github.com/yourname/go-tiny-claw/internal/schema"
)

// Session 把"对话历史 + 工作目录"提升为一级实体。history 私有 + RWMutex 保护，
// 避免外部直接持有 slice 引用造成 data race。同一个 AgentEngine 可服务多个不同
// WorkDir 的 Session（workspace 跟着 session 走，不跟着 engine 走）。
type Session struct {
	ID        string
	WorkDir   string
	CreatedAt time.Time
	UpdatedAt time.Time

	history []schema.Message
	mu      sync.RWMutex
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

// Append 是长期记忆的写入口（thinking / action / observation 都落这里）。
func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()
}

// GetWorkingMemory 返回短期工作记忆：末尾 limit 条的滑动窗口。
// 关键防御：若窗口首条是 ToolResult（RoleUser + ToolCallID），说明它对应的
// assistant tool_use 已被截断在窗口外——把"无主的 tool_result"发给 LLM API 会报错，
// 因此从头部一路剥掉孤儿，直到窗口首条是合法的 turn 起点。
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

// SessionManager 并发安全地按 ID（如 Slack channelID）管理 session 池。
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

// GlobalSessionMgr 是包级全局单例，方便各 IM adapter（Slack 等）共享同一 session 池。
var GlobalSessionMgr = &SessionManager{
	sessions: make(map[string]*Session),
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
