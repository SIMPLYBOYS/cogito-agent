// cmd/claw-demo-observability 是可觀測性/成本追蹤演示：用 decorator 模式把
// CostTracker 套在真實 LLMProvider 外面，引擎毫不知情地照常呼叫，而每次呼叫的 Token 與
// 費用被透明地計量、累加進 Session，跑完印出一張"財務報表"。
package main

import (
	"context"
	"log"
	"os"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("請先在 .env 或環境變數中設置 ANTHROPIC_API_KEY")
	}

	workDir := "/tmp/claw_observability_demo"
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("創建演示目錄失敗: %v", err)
	}

	modelName := "claude-opus-4-8"

	// 1. 真實的底層大腦
	realProvider := provider.NewClaudeProvider(modelName)

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate("test_observability_001", workDir)

	// 2. 核心拼裝：用 CostTracker 把真實 provider 包裹起來（它也實現 LLMProvider 介面）
	trackedProvider := observability.NewCostTracker(realProvider, modelName, sess)

	registry := tools.NewRegistry()
	registry.Register(tools.NewBashTool(workDir))

	// 3. 把"被包裹的 provider"注入引擎——引擎對計費層毫不知情
	eng := engine.NewAgentEngine(trackedProvider, registry, false, false)
	reporter := engine.NewTerminalReporter()

	prompt := `請用 bash 幫我用 date 命令查一下現在的時間。`

	log.Println("\n>>> 🚀 啟動帶儀表盤的可觀測性測試...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎執行崩潰: %v", err)
	}

	log.Printf("\n================ 財務報表 ================\n")
	log.Printf("會話 ID: %s\n", sess.ID)
	log.Printf("總消耗 Input Tokens : %d\n", sess.TotalPromptTokens)
	log.Printf("總消耗 Output Tokens: %d\n", sess.TotalCompletionTokens)
	log.Printf("總計費用 (USD): $%.6f\n", sess.TotalCostUSD)
	log.Printf("==========================================\n")
}
