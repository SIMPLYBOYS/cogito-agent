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
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/cmdutil"
	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

func main() {
	promptPtr := flag.String("prompt", "", "要交給 Agent 執行的任務描述（留空則用內置默認任務）")
	// 默認 ./workspace 而非書本的 "."：保持工作區沙箱、避免汙染本倉庫。需要時可顯式指定任意目錄。
	dirPtr := flag.String("dir", "./workspace", "Agent 工作區目錄")
	sessionPtr := flag.String("session", "cli-session", "會話 ID，支持斷點續傳")
	planPtr := flag.Bool("plan", false, "開啟 Plan Mode：狀態外部化到 PLAN.md / TODO.md")
	verifyPtr := flag.String("verify", "", "goal 循環：驗證 bash 指令（退出碼 0 = 目標達成）。設了即跑到通過或用盡次數")
	attemptsPtr := flag.Int("max-attempts", 5, "goal 循環最大嘗試次數")
	flag.Parse()

	// 無 -prompt 就印用法後退出，別偷偷跑一個寫死的任務去動使用者的工作區（過去的預設行為）。
	if strings.TrimSpace(*promptPtr) == "" {
		fmt.Fprintln(os.Stderr, "請用 -prompt \"你的任務\" 交辦任務。例如：")
		fmt.Fprintln(os.Stderr, "  claw-cli -prompt \"幫我寫一個 http server\"")
		fmt.Fprintln(os.Stderr, "  claw-cli -prompt \"繼續上次的任務\" -dir ./myproj -session task_001   # 指定工作區 + 斷點續傳")
		fmt.Fprintln(os.Stderr, "完整參數：claw-cli -h")
		os.Exit(2)
	}

	// 載入 .env + 初始化 OTel（單一 bootstrap，避免漏接 InitTracing）。flush 必須在退出前呼叫。
	flush := cmdutil.Bootstrap("cogito-agent-cli")
	defer flush()

	// 選擇 LLM provider（COGITO_PROVIDER：claude 預設 / openai 相容）。
	realProvider, modelName, errProv := provider.FromEnv()
	if errProv != nil {
		log.Fatal(errProv)
	}

	prompt := *promptPtr

	workDir, err := filepath.Abs(*dirPtr)
	if err != nil {
		log.Fatalf("解析工作區路徑失敗: %v", err)
	}

	fmt.Println("==================================================")
	fmt.Printf("🚀 cogito-agent CLI | 📁 工作區: %s\n", workDir)
	fmt.Println("==================================================")

	log.Printf("[provider] model=%s", modelName)

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
	registry.Register(tools.NewRecallTool(workDir))    // 長期記憶按需檢索（CLI 工作區即記憶來源）
	registry.Register(tools.NewBarChartTool())         // 數據可視化：等寬長條圖
	if os.Getenv("COGITO_SKILL_SYNTH") == "1" || os.Getenv("COGITO_MEMORY_SYNTH") == "1" || os.Getenv("COGITO_KG_SYNTH") == "1" {
		registry.Register(tools.NewConsolidateTool(trackedProvider, workDir, sess)) // agent 可主動沉澱（與 post-task hook 互補；產物仍 gated）
	}

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

	var runErr error
	if *verifyPtr != "" {
		runErr = runGoalLoop(eng, sess, reporter, *verifyPtr, workDir, *attemptsPtr)
	} else {
		runErr = eng.Run(context.Background(), sess, reporter)
	}

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
	// 記憶暫存區（apply 後放行為 .claw/memory/ 記錄），這就是「從真實互動中持續優化判斷決策」的落點。
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
			log.Printf("[evolve] 🧠 新增 %d 條提案記憶（%s）到 .claw/%s（需 apply 放行為長期記憶記錄才生效）", len(added), kind, evolve.ProposedMemoryFileName)
		default:
			log.Printf("[evolve] 本次未發現值得記入專案記憶的內容")
		}
	}

	// KG 關係抽取（opt-in）：任務後從記憶節點抽 typed 關係 → 提案邊（需 apply-edges 過 gate；每次任務多一次 LLM 呼叫）。
	if os.Getenv("COGITO_KG_SYNTH") == "1" {
		if n, err := evolve.NewRelationExtractor(trackedProvider, workDir).Extract(context.Background()); err != nil {
			log.Printf("[evolve] KG 關係抽取失敗（不影響任務結果）: %v", err)
		} else if n > 0 {
			log.Printf("[evolve] 🔗 新增 %d 條提案關係到 .claw/kg/edges.proposed.jsonl（需 apply-edges 過 gate）", n)
		}
	}

	flush() // 顯式 flush（log.Fatal 走 os.Exit 會略過 defer；defer 仍涵蓋正常返回，once 去重）

	if runErr != nil {
		log.Fatalf("\n💥 引擎運行崩潰: %v", runErr)
	}

	fmt.Println("\n==================================================")
	fmt.Printf("💰 Session 累計消耗: $%.6f | Token: Input %d, Output %d\n",
		sess.TotalCostUSD, sess.TotalPromptTokens, sess.TotalCompletionTokens)
	fmt.Println("==================================================")
}

// runGoalLoop 是 /goal 式的循環：跑 agent → 用 bash verify 判定目標是否達成（退出碼 0）→
// 沒達成就把 verify 的輸出當反饋追加進對話、重試，直到通過或用盡次數。
// ponytail: verify 輸出直接當下一輪反饋，不另叫 LLM 反思（省一次 API）。要更聰明的教訓再接 eval.ReflectOnFailure。
func runGoalLoop(eng *engine.AgentEngine, sess *ctxpkg.Session, reporter engine.Reporter, verifyCmd, workDir string, maxAttempts int) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := eng.Run(context.Background(), sess, reporter); err != nil {
			return err
		}
		out, ok := goalVerify(verifyCmd, workDir)
		if ok {
			log.Printf("[goal] ✅ 第 %d 次達成目標（`%s` 退出碼 0）", attempt, verifyCmd)
			return nil
		}
		log.Printf("[goal] ❌ 第 %d/%d 次驗證未過：%s", attempt, maxAttempts, strings.TrimSpace(out))
		if attempt < maxAttempts {
			sess.Append(schema.Message{Role: schema.RoleUser, Content: fmt.Sprintf(
				"目標驗證未通過：執行 `%s` 退出碼非 0，輸出：\n%s\n請據此修正後繼續，直到驗證通過。", verifyCmd, out)})
		}
	}
	return fmt.Errorf("達到最大嘗試次數 %d，目標仍未通過驗證 `%s`", maxAttempts, verifyCmd)
}

// goalVerify 在 workDir 跑一條 bash 驗證指令，退出碼 0 視為目標達成。
func goalVerify(cmd, workDir string) (output string, ok bool) {
	c := exec.Command("bash", "-c", cmd)
	c.Dir = workDir
	out, err := c.CombinedOutput()
	return string(out), err == nil
}
