// cmd/claw-demo 是會話演示 harness：用兩個併發 session 讓 session 行為眼見為憑——
// (1) 工作記憶滑動窗口會"遺忘"被擠出窗口的內容；(2) 跨 session 完全隔離。
// 這不是實用入口（實用場景見 cmd/claw 的 Slack 多頻道 session）。
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
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

	// 自包含：準備 Session A 的工作區與含密鑰的 README，便於直接觀察行為
	frontDir := "/tmp/project_front"
	if err := os.MkdirAll(frontDir, 0755); err != nil {
		log.Fatalf("創建演示目錄失敗: %v", err)
	}
	readme := "# 項目說明\n\n部署密鑰（僅本檔案記錄）：DEPLOY_KEY=sk-demo-9f3a-7c21\n"
	if err := os.WriteFile(filepath.Join(frontDir, "README.md"), []byte(readme), 0644); err != nil {
		log.Fatalf("寫入演示 README 失敗: %v", err)
	}

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")

	registry := tools.NewRegistry()
	// 已知侷限：tools 的 workDir 寫死為 Session A 的目錄，與 session.WorkDir 尚未對齊；
	// Session B 用"不準呼叫工具"規避（per-session 工具沙箱留待後續章節）。
	registry.Register(tools.NewReadFileTool(frontDir))

	eng := engine.NewAgentEngine(llmProvider, registry, false, false)
	reporter := engine.NewTerminalReporter()

	var wg sync.WaitGroup

	// ===== 場景 1：Session A —— 滑動窗口遺忘 =====
	wg.Add(1)
	go func() {
		defer wg.Done()
		sessionA := ctxpkg.GlobalSessionMgr.GetOrCreate("chat_front_001", frontDir)

		log.Println("\n>>> 🙋 [Session A / Turn 1]: 幫我看看 README.md 裡記錄了什麼密鑰？")
		sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "幫我看看 README.md 裡記錄了什麼密鑰？"})
		_ = eng.Run(context.Background(), sessionA, reporter)

		// 人為注水 6 對閒聊，把 Turn 1 的密鑰擠出 6 條滑動窗口
		for i := 0; i < 6; i++ {
			sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "這只是一句閒聊佔位符。"})
			sessionA.Append(schema.Message{Role: schema.RoleAssistant, Content: "好的，收到閒聊。"})
		}

		log.Println("\n>>> 🙋 [Session A / Turn 2]: 剛才第一輪查到的密鑰是什麼？（不準呼叫工具）")
		sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "請直接告訴我，剛才第一輪你查到的那個密鑰是什麼？不準呼叫工具！"})
		_ = eng.Run(context.Background(), sessionA, reporter)
	}()

	// ===== 場景 2：Session B —— 跨會話隔離 =====
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(1 * time.Second) // 讓 Session A 先跑，避免終端輸出交錯

		sessionB := ctxpkg.GlobalSessionMgr.GetOrCreate("chat_back_002", "/tmp/project_back")

		log.Println("\n>>> 🙋 [Session B]: 別人查到了一個密鑰，你這裡能看到嗎？（不準呼叫工具）")
		sessionB.Append(schema.Message{Role: schema.RoleUser, Content: "別人查到了一個密鑰，你這裡能看到嗎？不準呼叫工具！"})
		_ = eng.Run(context.Background(), sessionB, reporter)
	}()

	wg.Wait()
}
