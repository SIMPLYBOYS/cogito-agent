package eval

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
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
	TestCaseID     string   `json:"test_case_id"`
	Passed         bool     `json:"passed"`
	TotalCostUSD   float64  `json:"total_cost_usd"`
	DurationMs     int64    `json:"duration_ms"`
	TurnCount      int      `json:"turn_count"`       // 完成任務用了幾輪 ReAct（駕馭順滑度：一發入魂 vs 掙扎多輪）
	ToolErrorCount int      `json:"tool_error_count"` // 中途工具報錯次數（試錯成本：摔了幾跤）
	ErrorMsg       string   `json:"error_msg,omitempty"`
	Attempts       int      `json:"attempts"`          // 共嘗試幾次（Reflexion：>1 表示靠反思重試才過/仍敗）
	Lessons        []string `json:"lessons,omitempty"` // 每次失敗反思出、回注下一次的教訓
}

// SuiteReport 是一次完整跑分的機器可讀報告（供 CI 判定門檻、儀表板視覺化）。
type SuiteReport struct {
	Model        string       `json:"model"`
	GeneratedAt  string       `json:"generated_at"` // RFC3339
	Total        int          `json:"total"`
	Passed       int          `json:"passed"`
	PassRate     float64      `json:"pass_rate"` // 0..1
	TotalCostUSD float64      `json:"total_cost_usd"`
	Results      []TestResult `json:"results"`
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
	// MaxAttempts：Reflexion 重試上限。<=1 等於關閉（失敗就失敗）；>1 時失敗會反思出教訓、帶教訓重試。
	MaxAttempts int
}

func NewBenchmarkRunner(model string) *BenchmarkRunner {
	return &BenchmarkRunner{modelName: model, MaxAttempts: 1}
}

// RunSuite 順序執行一組評測集，打印跑分報告，並回傳機器可讀的 SuiteReport（供 CI/儀表板）。
func (b *BenchmarkRunner) RunSuite(ctx context.Context, testcases []TestCase) *SuiteReport {
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

	passRate := 0.0
	if len(testcases) > 0 {
		passRate = float64(passedCount) / float64(len(testcases))
	}

	log.Println("\n================ 🏆 跑分終極報告 ================")
	log.Printf("總用例數: %d | 成功數: %d | 成功率: %.2f%%\n", len(testcases), passedCount, passRate*100)
	log.Printf("總消耗成本: $%.6f\n", totalCost)
	log.Println("==================================================")

	return &SuiteReport{
		Model:        b.modelName,
		GeneratedAt:  time.Now().Format(time.RFC3339),
		Total:        len(testcases),
		Passed:       passedCount,
		PassRate:     passRate,
		TotalCostUSD: totalCost,
		Results:      results,
	}
}

// runSingleTest 跑一個用例。MaxAttempts>1 時啟用 Reflexion：失敗就反思出教訓、帶教訓重試，
// 直到通過或用盡次數。回傳最後一次的結果（含 Attempts 與歷次教訓）。
func (b *BenchmarkRunner) runSingleTest(ctx context.Context, tc TestCase) TestResult {
	maxAttempts := b.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lessons []string
	var last TestResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		prompt := injectLessons(tc.TaskPrompt, lessons) // 把歷次教訓回注到重試指令
		res, history, failOut := b.runOnce(ctx, tc, prompt, attempt)
		res.Attempts = attempt
		res.Lessons = append([]string(nil), lessons...)
		last = res

		if res.Passed {
			if attempt > 1 {
				log.Printf("    🔁 Reflexion：第 %d 次嘗試通過（靠 %d 條教訓）", attempt, len(lessons))
			}
			return res
		}
		if attempt >= maxAttempts {
			break
		}
		// 失敗 → 反思出一條教訓，回注下一次。反思失敗不致命，照樣重試。
		reflectProvider := provider.NewClaudeProvider(b.modelName)
		lesson, err := ReflectOnFailure(ctx, reflectProvider, tc.TaskPrompt, history, failOut)
		if err != nil {
			log.Printf("    ⚠️ 反思失敗（仍會重試）: %v", err)
		} else if lesson != "" {
			lessons = append(lessons, lesson)
			log.Printf("    🧠 教訓 #%d：%s", len(lessons), lesson)
		}
	}
	return last
}

// runOnce 跑一次嘗試：乾淨沙箱 + Setup + Agent 執行 + Validate。回傳結果、執行軌跡、失敗輸出。
func (b *BenchmarkRunner) runOnce(ctx context.Context, tc TestCase, taskPrompt string, attempt int) (TestResult, []schema.Message, string) {
	startTime := time.Now()

	// 每次嘗試一個絕對乾淨的沙箱目錄（含 attempt 後綴防撞名，重試不被前次汙染）。
	workDir, _ := os.Getwd()
	workDir += fmt.Sprintf("/workspace/%s_%d_%d", tc.ID, time.Now().Unix(), attempt)
	_ = os.MkdirAll(workDir, 0755)

	if tc.SetupScript != "" {
		cmd := exec.Command("bash", "-c", tc.SetupScript)
		cmd.Dir = workDir
		if err := cmd.Run(); err != nil {
			return TestResult{TestCaseID: tc.ID, Passed: false, ErrorMsg: "靶機 Setup 失敗"}, nil, "靶機 Setup 失敗"
		}
	}

	realProvider := provider.NewClaudeProvider(b.modelName)
	session := ctxpkg.NewSession(tc.ID, workDir)
	trackedProvider := observability.NewCostTracker(realProvider, b.modelName, session)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(trackedProvider, registry, false, false)
	if tc.MaxTurns > 0 {
		eng.MaxTurns = tc.MaxTurns
	}

	rep := &benchReporter{}
	session.Append(schema.Message{Role: schema.RoleUser, Content: taskPrompt})
	if err := eng.Run(ctx, session, rep); err != nil {
		errMsg := fmt.Sprintf("Agent 崩潰: %v", err)
		return TestResult{
			TestCaseID: tc.ID, Passed: false, TotalCostUSD: session.TotalCostUSD,
			DurationMs: time.Since(startTime).Milliseconds(), TurnCount: rep.turns, ToolErrorCount: rep.toolErrors,
			ErrorMsg: errMsg,
		}, session.GetWorkingMemory(0), errMsg
	}

	cmd := exec.Command("bash", "-c", tc.ValidateScript)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	duration := time.Since(startTime).Milliseconds()

	if err != nil {
		return TestResult{
			TestCaseID: tc.ID, Passed: false, TotalCostUSD: session.TotalCostUSD,
			DurationMs: duration, TurnCount: rep.turns, ToolErrorCount: rep.toolErrors,
			ErrorMsg: fmt.Sprintf("驗證腳本執行失敗: %s", string(out)),
		}, session.GetWorkingMemory(0), string(out)
	}

	return TestResult{
		TestCaseID: tc.ID, Passed: true, TotalCostUSD: session.TotalCostUSD,
		DurationMs: duration, TurnCount: rep.turns, ToolErrorCount: rep.toolErrors,
	}, session.GetWorkingMemory(0), ""
}
