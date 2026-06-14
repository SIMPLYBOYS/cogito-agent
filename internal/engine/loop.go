package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
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

	composer := ctxpkg.NewPromptComposer(session.WorkDir, e.PlanMode)
	systemMsg := composer.Build()

	for {
		availableTools := e.registry.GetAvailableTools()
		workingMemory := session.GetWorkingMemory(20)

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		// 【核心防线】发 LLM 前做字符级压缩；只动发给 LLM 的副本，不损毁 session.history。
		compactedContext := e.compactor.Compact(contextHistory)

		// 本轮 thinking 内容暂存（不单独进 session，最后合并进 action 消息）
		var currentTurnThinkingContent string

		// Phase 1: Thinking
		// 注意：手动两阶段思考（剥夺 tools）对 Claude 会退化成 <invoke> 文本，故各入口默认
		// EnableThinking=false；ch13 的合并逻辑也确保即便开启也不会产生连续两条 assistant。
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}

			thinkResp, err := e.provider.Generate(ctx, compactedContext, nil)
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
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
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

				result := e.registry.Execute(ctx, call)

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
