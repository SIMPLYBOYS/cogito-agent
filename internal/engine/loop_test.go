package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// concProbe 記錄「同時在 Execute 中」的工具峰值，用來驗證併發上限。
type concProbe struct {
	mu       sync.Mutex
	cur, max int
}

func (p *concProbe) Name() string                      { return "probe" }
func (p *concProbe) Definition() schema.ToolDefinition { return schema.ToolDefinition{Name: "probe"} }
func (p *concProbe) Execute(context.Context, json.RawMessage) (string, error) {
	p.mu.Lock()
	p.cur++
	if p.cur > p.max {
		p.max = p.cur
	}
	p.mu.Unlock()
	time.Sleep(20 * time.Millisecond) // 製造重疊窗口，讓併發真的發生
	p.mu.Lock()
	p.cur--
	p.mu.Unlock()
	return "ok", nil
}
func (p *concProbe) peak() int { p.mu.Lock(); defer p.mu.Unlock(); return p.max }

// batchProvider 在第 1 輪一次性吐出 n 個工具請求，第 2 輪結束。
type batchProvider struct {
	mu    sync.Mutex
	n     int
	calls int
}

func (p *batchProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	p.mu.Lock()
	p.calls++
	first := p.calls == 1
	p.mu.Unlock()
	if first {
		tcs := make([]schema.ToolCall, p.n)
		for i := range tcs {
			tcs[i] = schema.ToolCall{ID: fmt.Sprintf("t%d", i), Name: "probe", Arguments: []byte("{}")}
		}
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: tcs}, nil
	}
	return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
}

// 一次吐出 12 個工具、上限 3：同時在跑的峰值必須 ≤3，且確實達到併發（≥2）。
func TestRun_ToolConcurrencyIsCapped(t *testing.T) {
	probe := &concProbe{}
	reg := tools.NewRegistry()
	reg.Register(probe)

	eng := NewAgentEngine(&batchProvider{n: 12}, reg, false, false)
	eng.MaxConcurrentTools = 3
	eng.MaxCostUSD = 0

	sess := ctxpkg.NewSession("conc", t.TempDir())
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "go"})

	if err := eng.Run(context.Background(), sess, nil); err != nil {
		t.Fatalf("不應報錯: %v", err)
	}
	if pk := probe.peak(); pk > 3 {
		t.Errorf("同時併發峰值應 ≤3，實際 %d", pk)
	} else if pk < 2 {
		t.Errorf("12 個工具、上限 3，峰值應確實達到併發(≥2)，實際 %d（疑似被序列化）", pk)
	}
}

// MaxConcurrentTools=0 → 不限流（信號量為 no-op，不可死鎖）：8 個應大量同時併發。
func TestRun_ToolConcurrencyUnlimitedWhenZero(t *testing.T) {
	probe := &concProbe{}
	reg := tools.NewRegistry()
	reg.Register(probe)

	eng := NewAgentEngine(&batchProvider{n: 8}, reg, false, false)
	eng.MaxConcurrentTools = 0
	eng.MaxCostUSD = 0

	sess := ctxpkg.NewSession("conc0", t.TempDir())
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "go"})

	if err := eng.Run(context.Background(), sess, nil); err != nil {
		t.Fatalf("不應報錯（不限流不應死鎖）: %v", err)
	}
	if pk := probe.peak(); pk <= 3 {
		t.Errorf("不限流時 8 個應大量同時併發，峰值應 >3，實際 %d", pk)
	}
}

// captureProvider 抓下發給模型的 system prompt，並以「無工具調用」讓 Run 在第 1 輪即結束。
type captureProvider struct {
	system string
}

func (p *captureProvider) Generate(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	for _, m := range msgs {
		if m.Role == schema.RoleSystem {
			p.system = m.Content
		}
	}
	return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
}

// AssetsDir 有設：composer 應從 AssetsDir 讀 AGENTS.md，而非 session.WorkDir。
func TestRun_ComposerReadsAssetsDirWhenSet(t *testing.T) {
	assets := t.TempDir()
	work := t.TempDir() // 與 assets 不同、且為空
	if err := os.WriteFile(filepath.Join(assets, "AGENTS.md"), []byte("PROJECT_MARKER_XYZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	cp := &captureProvider{}
	eng := NewAgentEngine(cp, newTestRegistry(), false, false)
	eng.AssetsDir = assets
	eng.MaxCostUSD = 0

	sess := ctxpkg.NewSession("assets", work)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "go"})
	if err := eng.Run(context.Background(), sess, nil); err != nil {
		t.Fatalf("不應報錯: %v", err)
	}
	if !strings.Contains(cp.system, "PROJECT_MARKER_XYZ") {
		t.Error("composer 應從 AssetsDir 讀到 AGENTS.md（共享資產與 per-channel 工作目錄分離）")
	}
}

// AssetsDir 未設：回退到 session.WorkDir（CLI/demo 單一目錄行為不變）。
func TestRun_ComposerFallsBackToWorkDir(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("WORKDIR_MARKER_ABC"), 0o644); err != nil {
		t.Fatal(err)
	}

	cp := &captureProvider{}
	eng := NewAgentEngine(cp, newTestRegistry(), false, false)
	// 不設 AssetsDir
	eng.MaxCostUSD = 0

	sess := ctxpkg.NewSession("fallback", work)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "go"})
	if err := eng.Run(context.Background(), sess, nil); err != nil {
		t.Fatalf("不應報錯: %v", err)
	}
	if !strings.Contains(cp.system, "WORKDIR_MARKER_ABC") {
		t.Error("AssetsDir 未設時應回退到 session.WorkDir 讀 AGENTS.md")
	}
}
