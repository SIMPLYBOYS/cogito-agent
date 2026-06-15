package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// traceKey 是 Context 中存放當前 Span 的專屬 Key。
type traceKey struct{}

// Span 代表鏈路追蹤中的一個時間跨度與操作節點（樹形，通過 context 級聯父子關係）。
type Span struct {
	Name       string                 `json:"name"`
	StartTime  time.Time              `json:"start_time"`
	EndTime    time.Time              `json:"end_time"`
	DurationMs int64                  `json:"duration_ms"`
	Attributes map[string]interface{} `json:"attributes,omitempty"` // 元數據（如 Token、執行的命令）
	Children   []*Span                `json:"children,omitempty"`   // 子跨度

	mu sync.Mutex // 保護 Children 的併發寫入
}

// StartSpan 開啟一個新跨度：若 ctx 中已有父 Span 則掛為其子節點，並把自己作為新的當前 Span 放回衍生 ctx。
func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	span := &Span{
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
	}

	if parent, ok := ctx.Value(traceKey{}).(*Span); ok {
		parent.mu.Lock()
		parent.Children = append(parent.Children, span)
		parent.mu.Unlock()
	}

	newCtx := context.WithValue(ctx, traceKey{}, span)
	return newCtx, span
}

// EndSpan 結束跨度並計算耗時。
func (s *Span) EndSpan() {
	s.EndTime = time.Now()
	s.DurationMs = s.EndTime.Sub(s.StartTime).Milliseconds()
}

// AddAttribute 為當前 Span 記錄一條元數據。
func (s *Span) AddAttribute(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Attributes[key] = value
}

// ExportTraceToFile 把整棵 Span 樹序列化為 .claw/traces 下的 JSON 文件（便於回放排查）。
func ExportTraceToFile(rootSpan *Span, workDir string, sessionID string) error {
	traceDir := filepath.Join(workDir, ".claw", "traces")
	os.MkdirAll(traceDir, 0755)

	// 用 UnixNano 避免同一秒內多次運行文件碰撞
	filename := filepath.Join(traceDir, fmt.Sprintf("trace_%s_%d.json", sessionID, time.Now().UnixNano()))

	data, err := json.MarshalIndent(rootSpan, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filename, data, 0644)
}
