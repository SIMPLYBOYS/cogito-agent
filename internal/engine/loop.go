package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/observability"
	"github.com/yourname/go-tiny-claw/internal/provider"
	"github.com/yourname/go-tiny-claw/internal/schema"
	"github.com/yourname/go-tiny-claw/internal/tools"
)

// ch11: 引擎对 workspace 无状态（workspace 跟着 Session 走）。
// ch12: compactor —— 每次发 LLM 前做字符级压缩（OOM 防线）。
// ch13: PlanMode —— 状态外部化（PLAN.md / TODO.md）开关，透传给 composer。
type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	EnableThinking bool
	PlanMode       bool
	compactor      *ctxpkg.Compactor
	recovery       *ctxpkg.RecoveryManager // ch14: 工具错误自愈（注入救援指南）
	injector       *ReminderInjector       // ch15: 死循环探测与强提醒注入
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool, planMode bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		EnableThinking: enableThinking,
		PlanMode:       planMode,
		// ch13: 阈值从 ch12 演示用的 3000 提到 20000（≈6000 中文字），更贴近正常使用；
		// 生产环境仍建议按 model context window 自动计算或改用 token 级估算。
		compactor: ctxpkg.NewCompactor(20000, 6),
		recovery:  ctxpkg.NewRecoveryManager(),
		injector:  NewReminderInjector(),
	}
}

func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，工作区: %s\n", session.ID, session.WorkDir)

	// ch16: 把 session 注入 ctx，让工具 middleware 能取到触发它的会话（如审批要发回的 Slack 频道）
	ctx = WithSession(ctx, session)

	// ch19【埋点 1】Root Span：记录整个任务生命周期，退出时（无论成败）导出 trace 到 .claw/traces/
	ctx, rootSpan := observability.StartSpan(ctx, "Agent.Run")
	rootSpan.AddAttribute("SessionID", session.ID)
	rootSpan.AddAttribute("WorkDir", session.WorkDir)
	defer func() {
		rootSpan.EndSpan()
		_ = observability.ExportTraceToFile(rootSpan, session.WorkDir, session.ID)
		log.Printf("📊 [Tracing] 本次任务的执行回放链路已保存至工作区的 .claw/traces 目录下\n")
	}()

	composer := ctxpkg.NewPromptComposer(session.WorkDir, e.PlanMode)
	systemMsg := composer.Build()

	turnCount := 0
	for {
		turnCount++
		// ch19【埋点 2】Turn Span（defer 确保 break/return 也会结束并计入树）
		turnCtx, turnSpan := observability.StartSpan(ctx, fmt.Sprintf("Turn-%d", turnCount))
		defer turnSpan.EndSpan()

		availableTools := e.registry.GetAvailableTools()
		workingMemory := session.GetWorkingMemory(20)

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		// 【核心防线】发 LLM 前做字符级压缩；只动发给 LLM 的副本，不损毁 session.history。
		compactedContext := e.compactor.Compact(contextHistory)

		// ch19: 记录发给模型的实际上下文大小，有助于排查幻觉
		turnSpan.AddAttribute("context_message_count", len(compactedContext))

		// 本轮 thinking 内容暂存（不单独进 session，最后合并进 action 消息）
		var currentTurnThinkingContent string

		// Phase 1: Thinking
		// 注意：手动两阶段思考（剥夺 tools）对 Claude 会退化成 <invoke> 文本，故各入口默认
		// EnableThinking=false；ch13 的合并逻辑也确保即便开启也不会产生连续两条 assistant。
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(turnCtx)
			}

			// ch19【埋点 3】记录 Thinking 调用
			thinkCtx, thinkSpan := observability.StartSpan(turnCtx, "LLM.Thinking")
			thinkResp, err := e.provider.Generate(thinkCtx, compactedContext, nil)
			thinkSpan.EndSpan()
			if err != nil {
				return fmt.Errorf("Thinking 阶段失败: %w", err)
			}
			if thinkResp.Content != "" {
				currentTurnThinkingContent = thinkResp.Content
				// 仅本轮临时拼接，让 Phase 2 看到刚才的思考；不进 session、不持久化
				compactedContext = append(compactedContext, *thinkResp)
			}
		}

		// Phase 2: Action
		// ch19【埋点 4】记录 Action 调用
		actCtx, actSpan := observability.StartSpan(turnCtx, "LLM.Action")
		actionResp, err := e.provider.Generate(actCtx, compactedContext, availableTools)
		actSpan.EndSpan()
		if err != nil {
			return fmt.Errorf("Action 阶段失败: %w", err)
		}

		// ch13【核心修正】：把 thinking 与 action 合并成单一 assistant 消息进 session，
		// 保证 history 严格 user/assistant 交替（避免连续两条 assistant 被严格模式拒绝）。
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

		// ch15: 收集本轮第一个工具的调用与原始结果，供 ReminderInjector 做死循环指纹分析
		var lastToolCall schema.ToolCall
		var lastToolResult schema.ToolResult

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)

			go func(idx int, call schema.ToolCall) {
				defer wg.Done()

				if reporter != nil {
					reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
				}

				// ch19: 传 turnCtx，使并发工具的 Tool.Execute span 平行挂在当前 Turn 节点下
				result := e.registry.Execute(turnCtx, call)

				if idx == 0 {
					lastToolCall = call
					lastToolResult = result
				}

				// ch14【核心拦截与注入】出错时由 RecoveryManager 诊断并注入"救援指南"，
				// reporter 与 session.history 两处都用注入后的版本，保证人/模型/历史三者一致。
				finalOutput := result.Output
				if result.IsError {
					finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
				}

				if reporter != nil {
					displayOutput := finalOutput
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "... (已截断)"
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

		// 工具结果作为 user 消息进 session，保证下一轮 role 必然 user→assistant 交替
		session.Append(observationMsgs...)

		// ch15【死循环探测】：本轮工具若与历史同参数连续失败达阈值，注入强提醒。
		// 该提醒是普通 user 文本，会紧跟在 tool_results 之后——claude.go 会把它并入
		// 同一条 user 消息（tool_result 块 + 文本块），避免连续两条 user 违反交替。
		if reminderMsg := e.injector.CheckAndInject(lastToolCall, lastToolResult); reminderMsg != nil {
			session.Append(*reminderMsg)
		}
	}

	return nil
}

// RunSub 是为 SubagentTool 拉起的一次性、受限的 ReAct 循环（ch17）：
//   - 不依赖外部 Session，对话历史是局部变量，跑完即丢（上下文隔离的关键）；
//   - 工具集仅为 caller 传入的 readOnlyRegistry（能力沙箱）；
//   - 强制关闭慢思考，直接行动；有 maxSubTurns 硬上限防卡死；
//   - 返回值 string 即"探索报告"，作为 spawn_subagent 工具的输出回给主 agent。
//
// 满足 tools.AgentRunner 接口；reporter 用 any 规避包依赖，内部断言回 Reporter。
func (e *AgentEngine) RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry tools.Registry, reporter any) (string, error) {
	contextHistory := []schema.Message{
		{
			Role: schema.RoleSystem,
			Content: `你是一个专门负责深度探索的探路者 (Explorer Subagent)。
你的任务是根据主架构师的指令，在当前工作区内仔细阅读代码、查阅日志，搜集足够的信息。

【核心纪律】
1. 你必须、且只能依靠内置工具（如 bash 的 find/grep，或 read_file）去寻找答案。绝对不允许凭空捏造或猜测！
2. 如果你没有找到确切的答案，你必须继续使用工具深入搜索。
3. 当且仅当你找到了确切的线索后，停止调用工具，直接输出一段纯文本作为你的终极汇报。主架构师会根据你的汇报来做下一步决策。`,
		},
		{Role: schema.RoleUser, Content: taskPrompt},
	}

	const maxSubTurns = 10
	turnCount := 0

	for {
		turnCount++
		if turnCount > maxSubTurns {
			return "", fmt.Errorf("子智能体探索过于深入，超过 %d 轮被强制召回，请主 Agent 给它更明确的指令", maxSubTurns)
		}

		// 【驾驭底线】子智能体仅能使用传入的只读工具注册表（无 spawn_subagent → 无递归）
		availableTools := readOnlyRegistry.GetAvailableTools()
		compactedContext := e.compactor.Compact(contextHistory)

		// 子任务要求急速响应，强制跳过慢思考，直接行动
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return "", fmt.Errorf("子智能体推理失败: %w", err)
		}

		contextHistory = append(contextHistory, *actionResp)

		// 退出条件：不再调用工具 → 它已写好报告，直接把 Content 当返回值给主 agent
		if len(actionResp.ToolCalls) == 0 {
			return actionResp.Content, nil
		}

		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)
			go func(idx int, call schema.ToolCall) {
				defer wg.Done()

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
						display = display[:200] + "... (已截断)"
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
