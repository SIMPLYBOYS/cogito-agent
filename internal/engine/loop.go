package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	ctxpkg "github.com/yourname/go-tiny-claw/internal/context"
	"github.com/yourname/go-tiny-claw/internal/provider"
	"github.com/yourname/go-tiny-claw/internal/schema"
	"github.com/yourname/go-tiny-claw/internal/tools"
)

// ch11: 引擎对 workspace 无状态（workspace 跟着 Session 走）。
// ch12: 新增 compactor —— 每次发 LLM 前做字符级压缩（OOM 防线）。
type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	EnableThinking bool
	compactor      *ctxpkg.Compactor
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		EnableThinking: enableThinking,
		// 阈值 3000 字符 / 保护区 6 条沿用书中的演示用低值，便于触发压缩观察；
		// 生产环境应调高（或改用 token 级估算），以免误伤正常的工具输出。
		compactor: ctxpkg.NewCompactor(3000, 6),
	}
}

func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区: %s\n", session.ID, session.WorkDir)

	composer := ctxpkg.NewPromptComposer(session.WorkDir)
	systemMsg := composer.Build()

	for {
		availableTools := e.registry.GetAvailableTools()

		// ch12: 窗口放宽到 20 —— 消息条数交给滑动窗口，字符数量交给 compactor。
		workingMemory := session.GetWorkingMemory(20)

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		// 【核心防线】发 LLM 前做字符级压缩。compactor 只动"发给 LLM 的副本"，
		// 不损毁 session.history（长期记忆永远完整），下一轮重新取、重新压。
		compactedContext := e.compactor.Compact(contextHistory)

		// Phase 1: Thinking
		// 注意：手动两阶段思考（剥夺 tools）对 Claude 会退化成 <invoke> 文本而非结构化
		// tool_use，故各入口默认 EnableThinking=false；若要思考应改用原生 adaptive thinking。
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}

			thinkResp, err := e.provider.Generate(ctx, compactedContext, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段失败: %w", err)
			}
			if thinkResp.Content != "" {
				session.Append(*thinkResp) // 完整版进长期记忆
				compactedContext = append(compactedContext, *thinkResp)
			}
		}

		// Phase 2: Action
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段失败: %w", err)
		}

		session.Append(*actionResp) // 完整版进长期记忆
		compactedContext = append(compactedContext, *actionResp)

		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		if len(actionResp.ToolCalls) == 0 {
			break
		}

		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)

			go func(idx int, call schema.ToolCall) {
				defer wg.Done()

				if reporter != nil {
					reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
				}

				result := e.registry.Execute(ctx, call)

				if reporter != nil {
					displayOutput := result.Output
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "... (已截断)"
					}
					reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
				}

				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: call.ID,
				}
			}(i, toolCall)
		}

		wg.Wait()

		// 观察结果只 append 到 session（完整长期记忆）；下一轮 GetWorkingMemory + Compact 处理。
		session.Append(observationMsgs...)
	}

	return nil
}
