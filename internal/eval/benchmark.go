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
	MaxTurns       int    // 允許的最大輪數（目前未接入）
}

// TestResult 存放單次跑分結果。
type TestResult struct {
	TestCaseID   string
	Passed       bool
	TotalCostUSD float64
	DurationMs   int64
	ErrorMsg     string
}

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
			log.Printf(">>> ✅ 用例 [%s] 測試通過! | 耗時: %dms | 花費: $%.6f\n", tc.ID, res.DurationMs, res.TotalCostUSD)
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

	// 4. 讓 Agent 幹活；reporter 傳 nil 屏蔽刷屏（依賴各處 if reporter != nil 守衛）
	session.Append(schema.Message{Role: schema.RoleUser, Content: tc.TaskPrompt})
	if err := eng.Run(ctx, session, nil); err != nil {
		return TestResult{TestCaseID: tc.ID, Passed: false, ErrorMsg: fmt.Sprintf("Agent 崩潰: %v", err)}
	}

	// 5. 【核心斷言】用 ValidateScript 的 bash exit code 驗收成果
	cmd := exec.Command("bash", "-c", tc.ValidateScript)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()

	duration := time.Since(startTime).Milliseconds()

	if err != nil {
		return TestResult{
			TestCaseID:   tc.ID,
			Passed:       false,
			TotalCostUSD: session.TotalCostUSD,
			DurationMs:   duration,
			ErrorMsg:     fmt.Sprintf("驗證腳本執行失敗: %s", string(out)),
		}
	}

	return TestResult{
		TestCaseID:   tc.ID,
		Passed:       true,
		TotalCostUSD: session.TotalCostUSD,
		DurationMs:   duration,
	}
}
