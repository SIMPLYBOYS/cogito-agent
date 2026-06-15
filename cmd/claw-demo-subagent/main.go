// cmd/claw-demo-subagent 是 SubAgent（agent-as-tool）演示：主 agent 把"深度探索"
// 委派給一個受限的子 agent，子 agent 在隔離上下文裡搜索，只回傳一段精煉報告——主 agent 的
// 會話不被搜索過程的噪音汙染。自包含：啟動時在 /tmp/claw_subagent_demo 下鋪設尋寶 fixture。
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

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
		log.Fatal("請先在 .env 或環境變量中設置 ANTHROPIC_API_KEY")
	}

	// 自包含：鋪設尋寶 fixture —— 兩個誘餌 + 一個藏在深層目錄裡的"寶藏"
	workDir := "/tmp/claw_subagent_demo"
	fixtures := map[string]string{
		"fake1.go":                  "// 這是一個空文件\npackage main\n",
		"fake2.go":                  "// 這也是一個空文件\npackage main\n",
		"legacy/v1/auth/config.txt": "核心密碼是: super_secret_agent_password_42\n",
	}
	for rel, content := range fixtures {
		full := filepath.Join(workDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			log.Fatalf("創建演示目錄失敗: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			log.Fatalf("寫入 fixture 失敗: %v", err)
		}
	}

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")
	reporter := engine.NewTerminalReporter()

	// 【防禦沙箱】子智能體的受限只讀工具池：只有 read_file + bash（無 write/edit/spawn → 無遞歸）
	readOnlyRegistry := tools.NewRegistry()
	readOnlyRegistry.Register(tools.NewReadFileTool(workDir))
	readOnlyRegistry.Register(tools.NewBashTool(workDir))

	// 主智能體的全功能工具池
	mainRegistry := tools.NewRegistry()
	mainRegistry.Register(tools.NewReadFileTool(workDir))
	mainRegistry.Register(tools.NewWriteFileTool(workDir))
	mainRegistry.Register(tools.NewBashTool(workDir))
	mainRegistry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(llmProvider, mainRegistry, false, false)

	// 【核心裝配】把持有 engine + 只讀 registry 的 spawn_subagent 工具塞進主工具池
	mainRegistry.Register(tools.NewSubagentTool(eng, readOnlyRegistry, reporter))

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate("test_subagent_001", workDir)

	prompt := `
	我需要你在這個遺留項目裡，找到那個"核心密碼"。
	為了防止汙染主上下文，請你務必派出子智能體（spawn_subagent）去執行探索任務。
	你可以讓子智能體使用 bash 去查找當前目錄（及其所有子目錄）下名為 config.txt 的文件。
	子智能體拿到密碼向你彙報後，請你親自使用 write_file 工具，將密碼寫在根目錄的 answer.txt 裡。
	`

	log.Println("\n>>> 🚀 啟動多智能體協同測試...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎運行崩潰: %v", err)
	}
}
