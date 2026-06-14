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

// traceKey 是 Context 中存放当前 Span 的专属 Key。
type traceKey struct{}

// Span 代表链路追踪中的一个时间跨度与操作节点（树形，通过 context 级联父子关系）。
type Span struct {
	Name       string                 `json:"name"`
	StartTime  time.Time              `json:"start_time"`
	EndTime    time.Time              `json:"end_time"`
	DurationMs int64                  `json:"duration_ms"`
	Attributes map[string]interface{} `json:"attributes,omitempty"` // 元数据（如 Token、执行的命令）
	Children   []*Span                `json:"children,omitempty"`   // 子跨度

	mu sync.Mutex // 保护 Children 的并发写入
}

// StartSpan 开启一个新跨度：若 ctx 中已有父 Span 则挂为其子节点，并把自己作为新的当前 Span 放回衍生 ctx。
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

// EndSpan 结束跨度并计算耗时。
func (s *Span) EndSpan() {
	s.EndTime = time.Now()
	s.DurationMs = s.EndTime.Sub(s.StartTime).Milliseconds()
}

// AddAttribute 为当前 Span 记录一条元数据。
func (s *Span) AddAttribute(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Attributes[key] = value
}

// ExportTraceToFile 把整棵 Span 树序列化为 .claw/traces 下的 JSON 文件（便于回放排查）。
func ExportTraceToFile(rootSpan *Span, workDir string, sessionID string) error {
	traceDir := filepath.Join(workDir, ".claw", "traces")
	os.MkdirAll(traceDir, 0755)

	// 用 UnixNano 避免同一秒内多次运行文件碰撞
	filename := filepath.Join(traceDir, fmt.Sprintf("trace_%s_%d.json", sessionID, time.Now().UnixNano()))

	data, err := json.MarshalIndent(rootSpan, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filename, data, 0644)
}
