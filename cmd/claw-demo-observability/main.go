// cmd/claw-demo-observability 是 ch18 的可观测性/成本追踪演示：用 decorator 模式把
// CostTracker 套在真实 LLMProvider 外面，引擎毫不知情地照常调用，而每次调用的 Token 与
// 费用被透明地计量、累加进 Session，跑完打印一张"财务报表"。
package main

import (
	"context"
	"log"
	"os"

	"github.com/joho/godotenv"
	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/engine"
	"github.com/yourname/go-tiny-claw/internal/observability"
	"github.com/yourname/go-tiny-claw/internal/provider"
	"github.com/yourname/go-tiny-claw/internal/schema"
	"github.com/yourname/go-tiny-claw/internal/tools"
)

func main() {
	_ = godotenv.Load()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY")
	}

	workDir := "/tmp/claw_observability_demo"
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("创建演示目录失败: %v", err)
	}

	modelName := "claude-opus-4-8"

	// 1. 真实的底层大脑
	realProvider := provider.NewClaudeProvider(modelName)

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate("test_observability_001", workDir)

	// 2. 核心拼装：用 CostTracker 把真实 provider 包裹起来（它也实现 LLMProvider 接口）
	trackedProvider := observability.NewCostTracker(realProvider, modelName, sess)

	registry := tools.NewRegistry()
	registry.Register(tools.NewBashTool(workDir))

	// 3. 把"被包裹的 provider"注入引擎——引擎对计费层毫不知情
	eng := engine.NewAgentEngine(trackedProvider, registry, false, false)
	reporter := engine.NewTerminalReporter()

	prompt := `请用 bash 帮我用 date 命令查一下现在的时间。`

	log.Println("\n>>> 🚀 启动带仪表盘的可观测性测试...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}

	log.Printf("\n================ 财务报表 ================\n")
	log.Printf("会话 ID: %s\n", sess.ID)
	log.Printf("总消耗 Input Tokens : %d\n", sess.TotalPromptTokens)
	log.Printf("总消耗 Output Tokens: %d\n", sess.TotalCompletionTokens)
	log.Printf("总计费用 (USD): $%.6f\n", sess.TotalCostUSD)
	log.Printf("==========================================\n")
}
