package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	ctxpkg "github.com/SIMPLYBOYS/go-tiny-claw/internal/context"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/observability"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/provider"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/schema"
	"github.com/SIMPLYBOYS/go-tiny-claw/internal/tools"
)

// 主循環硬性防線的默認值：兩者都是框架層強制（不依賴模型自覺），<=0 表示關閉該防線。
const (
	defaultMaxTurns           = 40  // 單次 Run 的最大回合數
	defaultMaxCostUSD         = 1.0 // 單次 Run 的成本熔斷上限（USD）
	defaultMaxConcurrentTools = 5   // 單輪內工具的最大同時併發數
)

// 引擎對 workspace 無狀態（workspace 跟著 Session 走）。
// compactor —— 每次發 LLM 前做字符級壓縮（OOM 防線）。
// PlanMode —— 狀態外部化（PLAN.md / TODO.md）開關，透傳給 composer。
type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	EnableThinking bool
	PlanMode       bool
	MaxTurns       int     // 主循環硬性回合上限（防失控重試燒 API）；<=0 不限制
	MaxCostUSD     float64 // 單次 Run 的成本熔斷上限（USD）；<=0 不限制
	// 單輪內工具的最大同時併發數（信號量）；<=0 不限制。防模型一次吐大量工具請求時瞬間打爆
	// 下游（如網路工具撞 Rate Limit）。採 per-turn 而非 registry 全局，以避開 spawn_subagent
	// 的重入死鎖（持令牌的工具其內部 RunSub 又搶同一令牌）。
	MaxConcurrentTools int
	compactor          *ctxpkg.Compactor
	recovery           *ctxpkg.RecoveryManager // 工具錯誤自愈（注入救援指南）
	injector           *ReminderInjector       // 死循環探測與強提醒注入
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool, planMode bool) *AgentEngine {
	return &AgentEngine{
		provider:           p,
		registry:           r,
		EnableThinking:     enableThinking,
		PlanMode:           planMode,
		MaxTurns:           defaultMaxTurns,
		MaxCostUSD:         defaultMaxCostUSD,
		MaxConcurrentTools: defaultMaxConcurrentTools,
		// 閾值從演示用的 3000 提到 20000（≈6000 中文字），更貼近正常使用；
		// 生產環境仍建議按 model context window 自動計算或改用 token 級估算。
		compactor: ctxpkg.NewCompactor(200000, 6),
		recovery:  ctxpkg.NewRecoveryManager(),
		injector:  NewReminderInjector(),
	}
}

func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 喚醒會話 [%s]，工作區: %s\n", session.ID, session.WorkDir)

	// 把 session 注入 ctx，讓工具 middleware 能取到觸發它的會話（如審批要發回的 Slack 頻道）
	ctx = WithSession(ctx, session)

	// 【埋點 1】Root Span：記錄整個任務生命週期，退出時（無論成敗）導出 trace 到 .claw/traces/
	ctx, rootSpan := observability.StartSpan(ctx, "Agent.Run")
	rootSpan.AddAttribute("SessionID", session.ID)
	rootSpan.AddAttribute("WorkDir", session.WorkDir)
	defer func() {
		rootSpan.EndSpan()
		_ = observability.ExportTraceToFile(rootSpan, session.WorkDir, session.ID)
		log.Printf("📊 [Tracing] 本次任務的執行回放鏈路已保存至工作區的 .claw/traces 目錄下\n")
	}()

	composer := ctxpkg.NewPromptComposer(session.WorkDir, e.PlanMode)
	systemMsg := composer.Build()

	// per-task 成本熔斷的基準：快照本次 Run 進入時 session 的累計花費。成本檢查只比較
	// 「本次任務的增量」(現值 − 基準)，避免拿頻道累計花費去誤殺後續任務（session 長存、
	// 跨多則訊息只增不減；用累計值會讓頻道用久後每則新任務在第 1 回合就被永久鎖死）。
	startCostUSD := session.CostUSD()

	turnCount := 0
	for {
		turnCount++

		// 【硬防線①】回合數熔斷：主循環不再無上限 for{}，超過上限由框架強制中止，
		// 不指望模型自己停下。對齊 RunSub 的 maxSubTurns 思路。
		if e.MaxTurns > 0 && turnCount > e.MaxTurns {
			msg := fmt.Sprintf("⚠️ 任務已達最大回合數上限（%d 輪）仍未完成，為避免失控重試已強制中止。請拆解任務或補充更明確的指令後重試。", e.MaxTurns)
			if reporter != nil {
				reporter.OnMessage(ctx, msg)
			}
			return fmt.Errorf("達到最大回合數上限 %d，強制終止", e.MaxTurns)
		}

		// 【硬防線③】per-task 成本熔斷：只看本次任務的增量 (現值 − 進入時基準)，超預算即斷路。
		// 把 CostTracker 從「只記帳」升級為「斷路器」，控制單一任務盲目重試的金錢失控。
		if e.MaxCostUSD > 0 {
			if spent := session.CostUSD() - startCostUSD; spent > e.MaxCostUSD {
				msg := fmt.Sprintf("⚠️ 本次任務已花費 $%.4f，超過單任務預算上限 $%.2f，為控制成本已強制中止。", spent, e.MaxCostUSD)
				if reporter != nil {
					reporter.OnMessage(ctx, msg)
				}
				return fmt.Errorf("達到單任務成本上限 $%.2f（本次已花費 $%.4f），強制終止", e.MaxCostUSD, spent)
			}
		}

		// 【埋點 2】Turn Span（defer 確保 break/return 也會結束並計入樹）
		turnCtx, turnSpan := observability.StartSpan(ctx, fmt.Sprintf("Turn-%d", turnCount))
		defer turnSpan.EndSpan()

		availableTools := e.registry.GetAvailableTools()
		workingMemory := session.GetWorkingMemory(20)

		// 滑動窗口截斷後，首條可能變成 Assistant（違反 Anthropic「首條須為 user」/嚴格交替）。
		// 在頭部強行插入一條佔位 User 消息穩住協議。（與 GetWorkingMemory 的孤兒 tool_result
		// 剝離互補：那個處理"首條是無主 tool_result"，這個處理"首條是 assistant"。）
		if len(workingMemory) > 0 && workingMemory[0].Role != schema.RoleUser {
			dummyUser := schema.Message{
				Role:    schema.RoleUser,
				Content: "[系統佔位符] 這是為了保持上下文連貫性而注入的斷點標記。請繼續執行你剛才的任務。",
			}
			workingMemory = append([]schema.Message{dummyUser}, workingMemory...)
		}

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		// 【核心防線】發 LLM 前做字符級壓縮；只動發給 LLM 的副本，不損毀 session.history。
		compactedContext := e.compactor.Compact(contextHistory)

		// 記錄發給模型的實際上下文大小，有助於排查幻覺
		turnSpan.AddAttribute("context_message_count", len(compactedContext))

		// 本輪 thinking 內容暫存（不單獨進 session，最後合併進 action 消息）
		var currentTurnThinkingContent string

		// Phase 1: Thinking
		// 注意：手動兩階段思考（剝奪 tools）對 Claude 會退化成 <invoke> 文本，故各入口默認
		// EnableThinking=false；合併邏輯也確保即便開啟也不會產生連續兩條 assistant。
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(turnCtx)
			}

			// 【埋點 3】記錄 Thinking 調用
			thinkCtx, thinkSpan := observability.StartSpan(turnCtx, "LLM.Thinking")
			thinkResp, err := e.provider.Generate(thinkCtx, compactedContext, nil)
			thinkSpan.EndSpan()
			if err != nil {
				return fmt.Errorf("Thinking 階段失敗: %w", err)
			}
			if thinkResp.Content != "" {
				currentTurnThinkingContent = thinkResp.Content
				// 僅本輪臨時拼接，讓 Phase 2 看到剛才的思考；不進 session、不持久化
				compactedContext = append(compactedContext, *thinkResp)
			}
		}

		// Phase 2: Action
		// 【埋點 4】記錄 Action 調用
		actCtx, actSpan := observability.StartSpan(turnCtx, "LLM.Action")
		actionResp, err := e.provider.Generate(actCtx, compactedContext, availableTools)
		actSpan.EndSpan()
		if err != nil {
			return fmt.Errorf("Action 階段失敗: %w", err)
		}

		// 【核心修正】：把 thinking 與 action 合併成單一 assistant 消息進 session，
		// 保證 history 嚴格 user/assistant 交替（避免連續兩條 assistant 被嚴格模式拒絕）。
		finalAssistantMsg := schema.Message{
			Role:      schema.RoleAssistant,
			Content:   strings.TrimSpace(currentTurnThinkingContent + "\n" + actionResp.Content),
			ToolCalls: actionResp.ToolCalls,
		}
		session.Append(finalAssistantMsg)

		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		if len(actionResp.ToolCalls) == 0 {
			break
		}

		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup

		// 收集本輪【所有】工具的調用與原始結果（按 idx 各寫各的槽，無 data race），
		// 供 ReminderInjector 做死循環分析——並行調用的每一個都要納入，不再只看第一個。
		turnCalls := make([]schema.ToolCall, len(actionResp.ToolCalls))
		turnResults := make([]schema.ToolResult, len(actionResp.ToolCalls))

		// 【併發限流】緩衝 channel 當計數信號量：限制本輪同時在跑的工具數，不影響 WaitGroup 聚合。
		toolSem := newToolSemaphore(e.MaxConcurrentTools)

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)

			go func(idx int, call schema.ToolCall) {
				defer wg.Done()
				toolSem.acquire()       // 取令牌：已達上限就阻塞等待
				defer toolSem.release() // 跑完歸還

				if reporter != nil {
					reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
				}

				// 傳 turnCtx，使併發工具的 Tool.Execute span 平行掛在當前 Turn 節點下
				result := e.registry.Execute(turnCtx, call)

				turnCalls[idx] = call
				turnResults[idx] = result

				// 【核心攔截與注入】出錯時由 RecoveryManager 診斷並注入"救援指南"，
				// reporter 與 session.history 兩處都用注入後的版本，保證人/模型/歷史三者一致。
				finalOutput := result.Output
				if result.IsError {
					finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
				}

				if reporter != nil {
					displayOutput := finalOutput
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "... (已截斷)"
					}
					reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
				}

				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    finalOutput,
					ToolCallID: call.ID,
				}
			}(i, toolCall)
		}

		wg.Wait()

		// 工具結果作為 user 消息進 session，保證下一輪 role 必然 user→assistant 交替
		session.Append(observationMsgs...)

		// 【死循環探測】：本輪工具若與歷史同參數連續失敗達閾值，注入強提醒。
		// 該提醒是普通 user 文本，會緊跟在 tool_results 之後——claude.go 會把它併入
		// 同一條 user 消息（tool_result 塊 + 文本塊），避免連續兩條 user 違反交替。
		if reminderMsg := e.injector.CheckTurn(turnCalls, turnResults); reminderMsg != nil {
			session.Append(*reminderMsg)
		}
	}

	return nil
}

// RunSub 是為 SubagentTool 拉起的一次性、受限的 ReAct 循環：
//   - 不依賴外部 Session，對話歷史是局部變量，跑完即丟（上下文隔離的關鍵）；
//   - 工具集僅為 caller 傳入的 readOnlyRegistry（能力沙箱）；
//   - 強制關閉慢思考，直接行動；有 maxSubTurns 硬上限防卡死；
//   - 返回值 string 即"探索報告"，作為 spawn_subagent 工具的輸出回給主 agent。
//
// 滿足 tools.AgentRunner 接口；reporter 用 any 規避包依賴，內部斷言回 Reporter。
func (e *AgentEngine) RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry tools.Registry, reporter any) (string, error) {
	contextHistory := []schema.Message{
		{
			Role: schema.RoleSystem,
			Content: `你是一個專門負責深度探索的探路者 (Explorer Subagent)。
你的任務是根據主架構師的指令，在當前工作區內仔細閱讀代碼、查閱日誌，蒐集足夠的信息。

【核心紀律】
1. 你必須、且只能依靠內置工具（如 bash 的 find/grep，或 read_file）去尋找答案。絕對不允許憑空捏造或猜測！
2. 如果你沒有找到確切的答案，你必須繼續使用工具深入搜索。
3. 當且僅當你找到了確切的線索後，停止調用工具，直接輸出一段純文本作為你的終極彙報。主架構師會根據你的彙報來做下一步決策。`,
		},
		{Role: schema.RoleUser, Content: taskPrompt},
	}

	const maxSubTurns = 10
	turnCount := 0

	for {
		turnCount++
		if turnCount > maxSubTurns {
			return "", fmt.Errorf("子智能體探索過於深入，超過 %d 輪被強制召回，請主 Agent 給它更明確的指令", maxSubTurns)
		}

		// 【駕馭底線】子智能體僅能使用傳入的只讀工具註冊表（無 spawn_subagent → 無遞歸）
		availableTools := readOnlyRegistry.GetAvailableTools()
		compactedContext := e.compactor.Compact(contextHistory)

		// 子任務要求急速響應，強制跳過慢思考，直接行動
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return "", fmt.Errorf("子智能體推理失敗: %w", err)
		}

		contextHistory = append(contextHistory, *actionResp)

		// 退出條件：不再調用工具 → 它已寫好報告，直接把 Content 當返回值給主 agent
		if len(actionResp.ToolCalls) == 0 {
			return actionResp.Content, nil
		}

		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup

		// 子智能體的工具 fan-out 同樣限流（獨立於主循環的信號量——這正是 per-turn 設計
		// 避開重入死鎖的關鍵：主 turn 與 subagent turn 各持各的令牌池）。
		toolSem := newToolSemaphore(e.MaxConcurrentTools)

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)
			go func(idx int, call schema.ToolCall) {
				defer wg.Done()
				toolSem.acquire()
				defer toolSem.release()

				var r Reporter
				if reporter != nil {
					r, _ = reporter.(Reporter)
				}
				if r != nil {
					r.OnToolCall(ctx, fmt.Sprintf("[Subagent] %s", call.Name), string(call.Arguments))
				}

				result := readOnlyRegistry.Execute(ctx, call)

				finalOutput := result.Output
				if result.IsError {
					finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
				}

				if r != nil {
					display := finalOutput
					if len(display) > 200 {
						display = display[:200] + "... (已截斷)"
					}
					r.OnToolResult(ctx, fmt.Sprintf("[Subagent] %s", call.Name), display, result.IsError)
				}

				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    finalOutput,
					ToolCallID: call.ID,
				}
			}(i, toolCall)
		}

		wg.Wait()
		contextHistory = append(contextHistory, observationMsgs...)
	}
}

// toolSemaphore 是基於緩衝 channel 的計數信號量，用來限制工具的同時併發數。
// 容量 <=0 時 ch 為 nil，acquire/release 皆為 no-op（不限流）。值型別即可安全傳遞：
// channel 是引用型別，複製結構體仍共享同一個底層 channel。
type toolSemaphore struct {
	ch chan struct{}
}

func newToolSemaphore(max int) toolSemaphore {
	if max <= 0 {
		return toolSemaphore{}
	}
	return toolSemaphore{ch: make(chan struct{}, max)}
}

func (s toolSemaphore) acquire() {
	if s.ch != nil {
		s.ch <- struct{}{}
	}
}

func (s toolSemaphore) release() {
	if s.ch != nil {
		<-s.ch
	}
}
