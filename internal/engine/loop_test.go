package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/go-tiny-claw/internal/context"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/tools"
)

// fakeProvider 永遠回一個工具調用（製造一個本來會無限循環的 ReAct），可選地每輪累加成本，
// 用來驗證框架層的硬防線會在無上限循環中強制中止。
type fakeProvider struct {
	mu      sync.Mutex
	calls   int
	costPer float64
	sess    *ctxpkg.Session
}

func (f *fakeProvider) Generate(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.sess != nil && f.costPer > 0 {
		f.sess.RecordUsage(10, 10, f.costPer)
	}
	return &schema.Message{
		Role:      schema.RoleAssistant,
		ToolCalls: []schema.ToolCall{{ID: "1", Name: "noop", Arguments: []byte("{}")}},
	}, nil
}

type noopTool struct{}

func (noopTool) Name() string                                             { return "noop" }
func (noopTool) Definition() schema.ToolDefinition                        { return schema.ToolDefinition{Name: "noop"} }
func (noopTool) Execute(context.Context, json.RawMessage) (string, error) { return "ok", nil }

func newTestRegistry() tools.Registry {
	r := tools.NewRegistry()
	r.Register(noopTool{})
	return r
}

// 修補①：主循環不再是無上限 for{}——一個永遠調用工具的模型必須在 MaxTurns 被框架強制中止。
func TestRun_MaxTurnsCircuitBreaker(t *testing.T) {
	fp := &fakeProvider{}
	eng := NewAgentEngine(fp, newTestRegistry(), false, false)
	eng.MaxTurns = 5
	eng.MaxCostUSD = 0 // 本例只測回合數

	sess := ctxpkg.NewSession("maxturns", t.TempDir())
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "go"})

	err := eng.Run(context.Background(), sess, nil)
	if err == nil {
		t.Fatal("無限調用工具的循環應被 MaxTurns 強制中止並返回 error")
	}
	if !strings.Contains(err.Error(), "最大回合數") {
		t.Errorf("錯誤訊息應指明回合數熔斷: %v", err)
	}
	if fp.calls != 5 {
		t.Errorf("應恰好跑 5 輪（第 6 輪在 Generate 前就被攔），實際 Generate 次數=%d", fp.calls)
	}
}

// 修補③：成本累加超過 MaxCostUSD 時，框架層斷路，不再繼續燒 API。
func TestRun_CostCircuitBreaker(t *testing.T) {
	sess := ctxpkg.NewSession("cost", t.TempDir())
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "go"})

	fp := &fakeProvider{costPer: 0.3, sess: sess}
	eng := NewAgentEngine(fp, newTestRegistry(), false, false)
	eng.MaxTurns = 100 // 不讓回合數先觸發
	eng.MaxCostUSD = 1.0

	err := eng.Run(context.Background(), sess, nil)
	if err == nil {
		t.Fatal("累計成本超預算應被強制中止並返回 error")
	}
	if !strings.Contains(err.Error(), "成本上限") {
		t.Errorf("錯誤訊息應指明成本熔斷: %v", err)
	}
	// 0.3/輪：第 4 輪後累計 1.2>1.0，第 5 輪頂部斷路 → Generate 跑了 4 次
	if fp.calls != 4 {
		t.Errorf("應在累計 1.2>1.0 時於第 5 輪斷路，Generate 次數應=4，實際=%d", fp.calls)
	}
}

// per-task 化的關鍵保證：session 已有「前幾則任務」累計的花費（遠超上限）時，新任務只看
// 自己的增量，不應被歷史累計值誤殺。判別性測試——累計語意會在第 1 回合(calls=0)立即斷路，
// per-task 語意則照常跑到本次增量超標(calls=4)。
func TestRun_CostBreakerIsPerTask(t *testing.T) {
	sess := ctxpkg.NewSession("pertask", t.TempDir())
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "go"})
	sess.RecordUsage(0, 0, 5.0) // 模擬此頻道先前已累計 $5（>單任務上限 $1）

	fp := &fakeProvider{costPer: 0.3, sess: sess}
	eng := NewAgentEngine(fp, newTestRegistry(), false, false)
	eng.MaxTurns = 100
	eng.MaxCostUSD = 1.0

	err := eng.Run(context.Background(), sess, nil)
	if err == nil {
		t.Fatal("本次任務增量超預算時仍應中止")
	}
	if !strings.Contains(err.Error(), "成本上限") {
		t.Errorf("錯誤訊息應指明成本熔斷: %v", err)
	}
	if fp.calls != 4 {
		t.Errorf("per-task 應只算本次增量、跑滿 4 輪才斷路；若被歷史累計 $5 誤殺會=0。實際=%d", fp.calls)
	}
}
