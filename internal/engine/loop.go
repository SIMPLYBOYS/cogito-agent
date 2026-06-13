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

// ch11: 引擎不再持有 WorkDir / composer —— workspace 跟着 Session 走，引擎对 workspace 无状态。
type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	EnableThinking bool
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		EnableThinking: enableThinking,
	}
}

// Run 服务于一个 Session：工作目录与对话历史都由 session 携带。
// 调用方需先把用户输入 session.Append 进去，再调用 Run（Run 不再接收 userPrompt）。
func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区: %s\n", session.ID, session.WorkDir)

	// composer 每次 Run 用 session 的 workDir 重建：支持同一引擎服务多个不同 workspace 的 session，
	// 同时保留"workspace 内容改了下次 Run 就生效"的热载特性。
	composer := ctxpkg.NewPromptComposer(session.WorkDir)
	systemMsg := composer.Build()

	for {
		availableTools := e.registry.GetAvailableTools()

		// 短期工作记忆：滑动窗口只取最近 6 条；长期完整历史留在 session.history。
		workingMemory := session.GetWorkingMemory(6)

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		// Phase 1: Thinking
		// 注意：手动两阶段思考（剥夺 tools 让模型"先想"）对 Claude 会退化成 <invoke> 文本而非
		// 结构化 tool_use，故各入口默认 EnableThinking=false。若要思考，应改用 claude.go 的原生
		// adaptive thinking，而不是这个剥夺 tools 的 hack。
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}

			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段失败: %w", err)
			}
			if thinkResp.Content != "" {
				session.Append(*thinkResp) // 持久化到长期记忆
				contextHistory = append(contextHistory, *thinkResp)
			}
		}

		// Phase 2: Action
		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段失败: %w", err)
		}

		session.Append(*actionResp) // 持久化到长期记忆
		contextHistory = append(contextHistory, *actionResp)

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

		// 观察结果只 append 到 session（长期记忆）；下一轮由 GetWorkingMemory 自动取回。
		// 不再 append 到 contextHistory —— 短期窗口与长期记忆就此解耦。
		session.Append(observationMsgs...)
	}

	return nil
}
