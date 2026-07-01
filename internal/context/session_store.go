package context

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// SessionSnapshot 是 Session 的可序列化快照（持久化的最小完整狀態）。
type SessionSnapshot struct {
	ID                    string           `json:"id"`
	WorkDir               string           `json:"work_dir"`
	CreatedAt             string           `json:"created_at"` // RFC3339
	UpdatedAt             string           `json:"updated_at"`
	History               []schema.Message `json:"history"`
	Summary               string           `json:"summary,omitempty"`
	TotalPromptTokens     int              `json:"total_prompt_tokens"`
	TotalCompletionTokens int              `json:"total_completion_tokens"`
	TotalCostUSD          float64          `json:"total_cost_usd"`
}

// SessionStore 是 session 的持久化後端。Load 第二個回傳值表示是否存在。
type SessionStore interface {
	Load(id string) (*SessionSnapshot, bool, error)
	Save(snap *SessionSnapshot) error
	List() ([]string, error)
}

// FileSessionStore 以「一 session 一 JSON 檔」落地磁碟，原子寫（temp + rename）避免半截檔。
type FileSessionStore struct {
	dir string
	mu  sync.Mutex // 序列化同檔寫入
}

// StoreFromEnv 依 COGITO_SESSION_DIR 建立檔案型 store；未設則回 (nil, "")（純記憶體，預設行為）。
// 第二個回傳值是生效目錄，供啟動日誌。
func StoreFromEnv() (SessionStore, string) {
	dir := os.Getenv("COGITO_SESSION_DIR")
	if dir == "" {
		return nil, ""
	}
	store, err := NewFileSessionStore(dir)
	if err != nil {
		log.Printf("[Session] 啟用持久化失敗（退回純記憶體）: %v", err)
		return nil, ""
	}
	return store, dir
}

// NewFileSessionStore 建立（必要時 mkdir）一個以 dir 為根的檔案型 store。
func NewFileSessionStore(dir string) (*FileSessionStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("建立 session 目錄失敗: %w", err)
	}
	return &FileSessionStore{dir: dir}, nil
}

// fileName 由 id 推導穩定且檔案系統安全的檔名：可讀前綴 + 雜湊後綴（避免清洗後撞名）。
func fileName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	safe := b.String()
	if len(safe) > 40 {
		safe = safe[:40]
	}
	h := sha256.Sum256([]byte(id))
	return safe + "-" + hex.EncodeToString(h[:])[:8] + ".json"
}

func (s *FileSessionStore) path(id string) string { return filepath.Join(s.dir, fileName(id)) }

func (s *FileSessionStore) Load(id string) (*SessionSnapshot, bool, error) {
	data, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("讀取 session 失敗: %w", err)
	}
	var snap SessionSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, false, fmt.Errorf("解析 session 失敗（檔案可能損毀）: %w", err)
	}
	return &snap, true, nil
}

func (s *FileSessionStore) Save(snap *SessionSnapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 session 失敗: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	final := s.path(snap.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("寫入 session 暫存檔失敗: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("原子替換 session 檔失敗: %w", err)
	}
	return nil
}

// List 回傳目錄裡所有 session 的 ID（讀每個檔的內容取真實 ID，不依賴檔名反推）。
func (s *FileSessionStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var snap SessionSnapshot
		if json.Unmarshal(data, &snap) == nil && snap.ID != "" {
			ids = append(ids, snap.ID)
		}
	}
	return ids, nil
}
