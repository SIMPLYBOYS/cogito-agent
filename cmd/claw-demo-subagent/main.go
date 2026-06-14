// cmd/claw-demo-subagent 是 ch17 的 SubAgent（agent-as-tool）演示：主 agent 把"深度探索"
// 委派给一个受限的子 agent，子 agent 在隔离上下文里搜索，只回传一段精炼报告——主 agent 的
// 会话不被搜索过程的噪音污染。自包含：启动时在 /tmp/claw_subagent_demo 下铺设寻宝 fixture。
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
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY")
	}

	// 自包含：铺设寻宝 fixture —— 两个诱饵 + 一个藏在深层目录里的"宝藏"
	workDir := "/tmp/claw_subagent_demo"
	fixtures := map[string]string{
		"fake1.go":                  "// 这是一个空文件\npackage main\n",
		"fake2.go":                  "// 这也是一个空文件\npackage main\n",
		"legacy/v1/auth/config.txt": "核心密码是: super_secret_agent_password_42\n",
	}
	for rel, content := range fixtures {
		full := filepath.Join(workDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			log.Fatalf("创建演示目录失败: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			log.Fatalf("写入 fixture 失败: %v", err)
		}
	}

	llmProvider := provider.NewClaudeProvider("claude-opus-4-8")
	reporter := engine.NewTerminalReporter()

	// 【防御沙箱】子智能体的受限只读工具池：只有 read_file + bash（无 write/edit/spawn → 无递归）
	readOnlyRegistry := tools.NewRegistry()
	readOnlyRegistry.Register(tools.NewReadFileTool(workDir))
	readOnlyRegistry.Register(tools.NewBashTool(workDir))

	// 主智能体的全功能工具池
	mainRegistry := tools.NewRegistry()
	mainRegistry.Register(tools.NewReadFileTool(workDir))
	mainRegistry.Register(tools.NewWriteFileTool(workDir))
	mainRegistry.Register(tools.NewBashTool(workDir))
	mainRegistry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(llmProvider, mainRegistry, false, false)

	// 【核心装配】把持有 engine + 只读 registry 的 spawn_subagent 工具塞进主工具池
	mainRegistry.Register(tools.NewSubagentTool(eng, readOnlyRegistry, reporter))

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate("test_subagent_001", workDir)

	prompt := `
	我需要你在这个遗留项目里，找到那个"核心密码"。
	为了防止污染主上下文，请你务必派出子智能体（spawn_subagent）去执行探索任务。
	你可以让子智能体使用 bash 去查找当前目录（及其所有子目录）下名为 config.txt 的文件。
	子智能体拿到密码向你汇报后，请你亲自使用 write_file 工具，将密码写在根目录的 answer.txt 里。
	`

	log.Println("\n>>> 🚀 启动多智能体协同测试...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
