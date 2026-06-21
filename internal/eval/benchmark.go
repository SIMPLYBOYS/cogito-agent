package eval

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/go-tiny-claw/internal/context"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/engine"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/observability"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/provider"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/tools"
)

// TestCase 用 SetupScript→TaskPrompt→ValidateScript 三段式定義一個待 Agent 完成並驗證的任務。
// 判定真相是 ValidateScript 的 bash exit code：exit 0 = 通過（通用、語言無關、可組合）。
type TestCase struct {
	ID             string // 用例唯一標識
	Name           string // 用例名稱
	SetupScript    string // 【可選】Agent 運行前執行的 bash（準備靶機文件）
	TaskPrompt     string // 發送給 Agent 的任務指令
	ValidateScript string // 【核心】Agent 運行後執行的 bash 校驗腳本，exit 0 視為成功
	MaxTurns       int    // 允許的最大輪數；>0 時覆蓋引擎默認回合上限（<=0 用引擎默認）
}

// TestResult 存放單次跑分結果。除了「結果/總成本/總耗時」，還收集兩個【過程】指標來區分
// 「一發入魂」與「重試多次才過」——它們與 Passed 正交，量測架構（Prompt/工具設計）的順滑度。
type TestResult struct {
	TestCaseID     string
	Passed         bool
	TotalCostUSD   float64
	DurationMs     int64
	TurnCount      int // 完成任務用了幾輪 ReAct（駕馭順滑度：一發入魂 vs 掙扎多輪）
	ToolErrorCount int // 中途工具報錯次數（試錯成本：摔了幾跤）
	ErrorMsg       string
}

// benchReporter 是跑分專用的計數 Reporter：靜默收集「回合數」與「工具報錯次數」，不刷屏。
type benchReporter struct {
	turns      int
	toolErrors int
}

func (r *benchReporter) OnTurn(ctx context.Context, turn int) {
	if turn > r.turns {
		r.turns = turn
	}
}
func (r *benchReporter) OnToolResult(ctx context.Context, toolName, result string, isError bool) {
	if isError {
		r.toolErrors++
	}
}
func (r *benchReporter) OnThinking(ctx context.Context)                        {}
func (r *benchReporter) OnToolCall(ctx context.Context, toolName, args string) {}
func (r *benchReporter) OnMessage(ctx context.Context, content string)         {}

type BenchmarkRunner struct {
	modelName string
}

func NewBenchmarkRunner(model string) *BenchmarkRunner {
	return &BenchmarkRunner{modelName: model}
}

// RunSuite 順序執行一組評測集並打印跑分報告。
func (b *BenchmarkRunner) RunSuite(ctx context.Context, testcases []TestCase) {
	log.Println("==================================================")
	log.Printf("🚀 啟動自動化 Harness Benchmark 評估... | 模型: %s\n", b.modelName)
	log.Println("==================================================")

	var results []TestResult
	passedCount := 0
	totalCost := 0.0

	for _, tc := range testcases {
		log.Printf("\n>>> ⏳ 正在執行用例 [%s]: %s\n", tc.ID, tc.Name)

		res := b.runSingleTest(ctx, tc)
		results = append(results, res)

		if res.Passed {
			passedCount++
			log.Printf(">>> ✅ 用例 [%s] 測試通過! | 回合: %d | 試錯(工具報錯): %d | 耗時: %dms | 花費: $%.6f\n",
				tc.ID, res.TurnCount, res.ToolErrorCount, res.DurationMs, res.TotalCostUSD)
		} else {
			log.Printf(">>> ❌ 用例 [%s] 測試失敗! | 錯誤: %s\n", tc.ID, res.ErrorMsg)
		}
		totalCost += res.TotalCostUSD
	}

	log.Println("\n================ 🏆 跑分終極報告 ================")
	log.Printf("總用例數: %d | 成功數: %d | 成功率: %.2f%%\n", len(testcases), passedCount, float64(passedCount)/float64(len(testcases))*100)
	log.Printf("總消耗成本: $%.6f\n", totalCost)
	log.Println("==================================================")
}

func (b *BenchmarkRunner) runSingleTest(ctx context.Context, tc TestCase) TestResult {
	startTime := time.Now()

	// 1. 為每個用例創建一個絕對乾淨的沙箱目錄（時間戳後綴防撞名，物理隔離不跨例汙染）
	workDir, _ := os.Getwd()
	workDir += fmt.Sprintf("/workspace/%s_%d", tc.ID, time.Now().Unix())
	_ = os.MkdirAll(workDir, 0755)

	// 2. （可選）執行 Setup 準備靶機代碼
	if tc.SetupScript != "" {
		cmd := exec.Command("bash", "-c", tc.SetupScript)
		cmd.Dir = workDir
		if err := cmd.Run(); err != nil {
			return TestResult{TestCaseID: tc.ID, Passed: false, ErrorMsg: "靶機 Setup 失敗"}
		}
	}

	// 3. 組裝具備計費打點能力的引擎（per-case 全新實例：provider/session/registry/engine 都是新的）
	realProvider := provider.NewClaudeProvider(b.modelName) // 真實的 Claude API
	session := ctxpkg.NewSession(tc.ID, workDir)            // 為本次跑分單獨建 Session 記賬
	trackedProvider := observability.NewCostTracker(realProvider, b.modelName, session)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(trackedProvider, registry, false, false)
	if tc.MaxTurns > 0 {
		eng.MaxTurns = tc.MaxTurns // 用例可覆蓋回合上限（複雜任務放寬、簡單任務收緊以暴露低效）
	}

	// 4. 讓 Agent 幹活；用計數 Reporter 靜默收集回合數與工具報錯次數（過程指標）
	rep := &benchReporter{}
	session.Append(schema.Message{Role: schema.RoleUser, Content: tc.TaskPrompt})
	if err := eng.Run(ctx, session, rep); err != nil {
		return TestResult{
			TestCaseID:     tc.ID,
			Passed:         false,
			TotalCostUSD:   session.TotalCostUSD,
			DurationMs:     time.Since(startTime).Milliseconds(),
			TurnCount:      rep.turns,
			ToolErrorCount: rep.toolErrors,
			ErrorMsg:       fmt.Sprintf("Agent 崩潰: %v", err),
		}
	}

	// 5. 【核心斷言】用 ValidateScript 的 bash exit code 驗收成果
	cmd := exec.Command("bash", "-c", tc.ValidateScript)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()

	duration := time.Since(startTime).Milliseconds()

	if err != nil {
		return TestResult{
			TestCaseID:     tc.ID,
			Passed:         false,
			TotalCostUSD:   session.TotalCostUSD,
			DurationMs:     duration,
			TurnCount:      rep.turns,
			ToolErrorCount: rep.toolErrors,
			ErrorMsg:       fmt.Sprintf("驗證腳本執行失敗: %s", string(out)),
		}
	}

	return TestResult{
		TestCaseID:     tc.ID,
		Passed:         true,
		TotalCostUSD:   session.TotalCostUSD,
		DurationMs:     duration,
		TurnCount:      rep.turns,
		ToolErrorCount: rep.toolErrors,
	}
}
