// cmd/claw-demo 是 ch11 的会话演示 harness：用两个并发 session 让 session 行为眼见为凭——
// (1) 工作记忆滑动窗口会"遗忘"被挤出窗口的内容；(2) 跨 session 完全隔离。
// 这不是实用入口（实用场景见 cmd/claw 的 Slack 多频道 session）。
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/joho/godotenv"
	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/engine"
	"github.com/yourname/go-tiny-claw/internal/provider"
	"github.com/yourname/go-tiny-claw/internal/schema"
	"github.com/yourname/go-tiny-claw/internal/tools"
)

func main() {
	_ = godotenv.Load()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY")
	}

	// 自包含：准备 Session A 的工作区与含密钥的 README，便于直接观察行为
	frontDir := "/tmp/project_front"
	if err := os.MkdirAll(frontDir, 0755); err != nil {
		log.Fatalf("创建演示目录失败: %v", err)
	}
	readme := "# 项目说明\n\n部署密钥（仅本文件记录）：DEPLOY_KEY=sk-demo-9f3a-7c21\n"
	if err := os.WriteFile(filepath.Join(frontDir, "README.md"), []byte(readme), 0644); err != nil {
		log.Fatalf("写入演示 README 失败: %v", err)
	}

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()
	// ch11 已知局限：tools 的 workDir 写死为 Session A 的目录，与 session.WorkDir 尚未对齐；
	// Session B 用"不准调用工具"规避（per-session 工具沙箱留待后续章节）。
	registry.Register(tools.NewReadFileTool(frontDir))

	eng := engine.NewAgentEngine(llmProvider, registry, false, false)
	reporter := engine.NewTerminalReporter()

	var wg sync.WaitGroup

	// ===== 场景 1：Session A —— 滑动窗口遗忘 =====
	wg.Add(1)
	go func() {
		defer wg.Done()
		sessionA := ctxpkg.GlobalSessionMgr.GetOrCreate("chat_front_001", frontDir)

		log.Println("\n>>> 🙋 [Session A / Turn 1]: 帮我看看 README.md 里记录了什么密钥？")
		sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "帮我看看 README.md 里记录了什么密钥？"})
		_ = eng.Run(context.Background(), sessionA, reporter)

		// 人为注水 6 对闲聊，把 Turn 1 的密钥挤出 6 条滑动窗口
		for i := 0; i < 6; i++ {
			sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "这只是一句闲聊占位符。"})
			sessionA.Append(schema.Message{Role: schema.RoleAssistant, Content: "好的，收到闲聊。"})
		}

		log.Println("\n>>> 🙋 [Session A / Turn 2]: 刚才第一轮查到的密钥是什么？（不准调用工具）")
		sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "请直接告诉我，刚才第一轮你查到的那个密钥是什么？不准调用工具！"})
		_ = eng.Run(context.Background(), sessionA, reporter)
	}()

	// ===== 场景 2：Session B —— 跨会话隔离 =====
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(1 * time.Second) // 让 Session A 先跑，避免终端输出交错

		sessionB := ctxpkg.GlobalSessionMgr.GetOrCreate("chat_back_002", "/tmp/project_back")

		log.Println("\n>>> 🙋 [Session B]: 别人查到了一个密钥，你这里能看到吗？（不准调用工具）")
		sessionB.Append(schema.Message{Role: schema.RoleUser, Content: "别人查到了一个密钥，你这里能看到吗？不准调用工具！"})
		_ = eng.Run(context.Background(), sessionB, reporter)
	}()

	wg.Wait()
}
