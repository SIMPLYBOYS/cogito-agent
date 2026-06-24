// cmd/claw-cli 是升級後的生產級命令行入口：把前面累積的全部能力組裝成一個通用 CLI。
// 事件經 TerminalReporter 實時打到 stdout；provider 外掛 CostTracker 自動記賬；trace 經 OTel
// 產生（設 OTEL_EXPORTER_OTLP_ENDPOINT 才上報，否則 no-op）；結束打印花費 + token 報表。
//
// 用法：
//
//	go run ./cmd/claw-cli -prompt "幫我寫一個 web server"
//	go run ./cmd/claw-cli -prompt "繼續上次的任務" -dir ./myproj -session task_001   # 指定工作區 + 斷點續傳
//	go run ./cmd/claw-cli -plan -prompt "..."                                       # 開 Plan Mode
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
	"github.com/joho/godotenv"
)

func main() {
	promptPtr := flag.String("prompt", "", "要交給 Agent 執行的任務描述（留空則用內置默認任務）")
	// 默認 ./workspace 而非書本的 "."：保持工作區沙箱、避免汙染本倉庫。需要時可顯式指定任意目錄。
	dirPtr := flag.String("dir", "./workspace", "Agent 工作區目錄")
	sessionPtr := flag.String("session", "cli-session", "會話 ID，支持斷點續傳")
	planPtr := flag.Bool("plan", false, "開啟 Plan Mode：狀態外部化到 PLAN.md / TODO.md")
	flag.Parse()

	_ = godotenv.Load()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("請先在 .env 或環境變量中設置 ANTHROPIC_API_KEY")
	}

	prompt := *promptPtr
	if prompt == "" {
		// 內置默認任務
		prompt = `
	我需要在當前目錄下新建一個 ping.go，提供一個簡單的 http ping 接口。
	寫完之後，幫我把代碼用 git 提交一下。
	`
	}

	workDir, err := filepath.Abs(*dirPtr)
	if err != nil {
		log.Fatalf("解析工作區路徑失敗: %v", err)
	}

	fmt.Println("==================================================")
	fmt.Printf("🚀 cogito-agent CLI | 📁 工作區: %s\n", workDir)
	fmt.Println("==================================================")

	modelName := "claude-opus-4-8"
	realProvider := provider.NewClaudeProvider(modelName)

	// session 持久化：設 COGITO_SESSION_DIR 即把歷史/費用落地磁碟——讓 -session 斷點續傳跨重啟生效。
	// 必須在 GetOrCreate 之前 SetStore，才能從磁碟復原既有 session。
	if store, dir := ctxpkg.StoreFromEnv(); store != nil {
		ctxpkg.GlobalSessionMgr.SetStore(store)
		log.Printf("[Session] 持久化已啟用: %s", dir)
	}

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(*sessionPtr, workDir)

	// 用 CostTracker 包裹 provider 自動記賬；trace 由 engine.Run 內部自動導出
	trackedProvider := observability.NewCostTracker(realProvider, modelName, sess)

	// 沙箱執行器：COGITO_SANDBOX=docker 時 bash 命令丟進隔離容器，否則宿主機直跑。
	executor := sandbox.FromEnv()
	log.Printf("[sandbox] bash 執行模式: %s", sandbox.Describe(executor))
	if c, ok := executor.(interface{ Close() error }); ok {
		defer c.Close() // 退出時移除 per-session sandbox 容器（docker 模式）
	}

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashToolWithExecutor(workDir, executor))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewReadSkillTool(workDir)) // 技能按需載入（CLI 工作區即技能來源）

	// 背景任務工具：長命命令（dev server / 長建置）不受 bash 30s 逾時限制。退出時統一收掉。
	taskMgr := tools.NewTaskManager(executor, workDir)
	for _, tt := range tools.NewTaskTools(taskMgr) {
		registry.Register(tt)
	}
	defer taskMgr.KillAll()

	eng := engine.NewAgentEngine(trackedProvider, registry, false, *planPtr)
	reporter := engine.NewTerminalReporter()

	fmt.Printf("\n🎯 收到任務: %s\n\n", prompt)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	runErr := eng.Run(context.Background(), sess, reporter)

	// Tier 4 技能自生成（opt-in）：任務【成功】後反思軌跡，把可複用流程寫成「提案技能」到暫存區。
	// 安全鐵律：只寫 .claw/skills-proposed/（不自動啟用），需人工 review 後手動移到 .claw/skills/。
	if os.Getenv("COGITO_SKILL_SYNTH") == "1" && runErr == nil {
		proposedDir := filepath.Join(workDir, ".claw", evolve.ProposedSkillsDirName)
		synth := evolve.NewSkillSynthesizer(trackedProvider, proposedDir)
		if path, err := synth.Reflect(context.Background(), prompt, sess.GetWorkingMemory(0)); err != nil {
			log.Printf("[evolve] 技能反思失敗（不影響任務結果）: %v", err)
		} else if path != "" {
			log.Printf("[evolve] 💡 已產出提案技能：%s（需人工 review 後移到 .claw/skills/ 才生效）", path)
		} else {
			log.Printf("[evolve] 本次任務未發現值得保存的可複用技能")
		}
	}

	// Tier 4 記憶自更新（opt-in）：成功→萃取耐久慣例；失敗→live Reflexion 萃取教訓。皆追加到提案
	// 記憶暫存區（不自動併入 AGENTS.md），這就是「從真實互動中持續優化判斷決策」的落點。
	if os.Getenv("COGITO_MEMORY_SYNTH") == "1" {
		mSynth := evolve.NewMemorySynthesizer(trackedProvider, workDir)
		var added []string
		var err error
		if runErr != nil {
			added, err = mSynth.ReflectFailure(context.Background(), prompt, sess.GetWorkingMemory(0), runErr.Error())
		} else {
			added, err = mSynth.Reflect(context.Background(), prompt, sess.GetWorkingMemory(0))
		}
		switch {
		case err != nil:
			log.Printf("[evolve] 記憶反思失敗（不影響任務結果）: %v", err)
		case len(added) > 0:
			kind := "慣例"
			if runErr != nil {
				kind = "失敗教訓"
			}
			log.Printf("[evolve] 🧠 新增 %d 條提案記憶（%s）到 .claw/%s（需人工 review 後併入 AGENTS.md）", len(added), kind, evolve.ProposedMemoryFileName)
		default:
			log.Printf("[evolve] 本次未發現值得記入專案記憶的內容")
		}
	}

	if runErr != nil {
		log.Fatalf("\n💥 引擎運行崩潰: %v", runErr)
	}

	fmt.Println("\n==================================================")
	fmt.Printf("💰 Session 累計消耗: $%.6f | Token: Input %d, Output %d\n",
		sess.TotalCostUSD, sess.TotalPromptTokens, sess.TotalCompletionTokens)
	fmt.Println("==================================================")
}
