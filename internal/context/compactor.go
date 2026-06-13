package context

import (
	"fmt"
	"log"

	"github.com/yourname/go-tiny-claw/internal/schema"
)

// Compactor 是字符级的上下文压缩防线：在每次发 LLM 前，对"要发出去的窗口"按
// 角色 × 位置 × 长度 三维规则压缩。它只动发给 LLM 的副本，不损毁 session.history。
//
// 与 ch11 滑动窗口的分工：窗口（GetWorkingMemory）防"历史太长"（消息条数），
// Compactor 防"单条消息太大"（字符数，OOM 元凶通常是早期读进来的大文件/巨型日志）。
type Compactor struct {
	MaxChars       int // 压缩触发阈值（总字符数）
	RetainLastMsgs int // 保护区大小（末 N 条不被严重清理）
}

func NewCompactor(maxChars int, retainLastMsgs int) *Compactor {
	return &Compactor{
		MaxChars:       maxChars,
		RetainLastMsgs: retainLastMsgs,
	}
}

func (c *Compactor) Compact(msgs []schema.Message) []schema.Message {
	currentLength := c.estimateLength(msgs)

	if currentLength < c.MaxChars {
		return msgs
	}

	log.Printf("[Compactor] ⚠️ 内存告警：当前上下文长度 (%d 字符) 超过阈值 (%d)，触发压缩清理...\n", currentLength, c.MaxChars)

	var compacted []schema.Message
	msgCount := len(msgs)

	protectStartIndex := msgCount - c.RetainLastMsgs
	if protectStartIndex < 0 {
		protectStartIndex = 0
	}

	for i, msg := range msgs {
		if msg.Role == schema.RoleSystem {
			compacted = append(compacted, msg) // system 永远完整
			continue
		}

		newMsg := msg
		isInWorkingMemory := i >= protectStartIndex

		if msg.Role == schema.RoleUser && msg.ToolCallID != "" {
			// tool_result 是最大威胁：早期的整段替换为占位符，近期的头尾保留
			if !isInWorkingMemory {
				if len(msg.Content) > 200 {
					newMsg.Content = fmt.Sprintf("...[为了节省内存，早期的工具输出已被系统强制清理。原始长度: %d 字节]...", len(msg.Content))
				}
			} else {
				const maxKeep = 1000
				if len(msg.Content) > maxKeep {
					head := msg.Content[:500]
					tail := msg.Content[len(msg.Content)-500:]
					newMsg.Content = fmt.Sprintf("%s\n\n...[内容过长，中间 %d 字节已被系统截断]...\n\n%s", head, len(msg.Content)-maxKeep, tail)
				}
			}
		} else if msg.Role == schema.RoleAssistant && msg.Content != "" {
			// 早期的 assistant 思考折叠为一句话；近期的完整保留。
			// 注意：不动 ToolCalls —— tool_use 结构必须与后续 tool_result 配对。
			if !isInWorkingMemory && len(msg.Content) > 200 {
				newMsg.Content = "...[早期的推理思考过程已折叠]..."
			}
		}

		compacted = append(compacted, newMsg)
	}

	newLength := c.estimateLength(compacted)
	log.Printf("[Compactor] ✅ 压缩完成。上下文长度从 %d 降至 %d 字符。\n", currentLength, newLength)

	return compacted
}

// estimateLength 是字符级（按 byte）的快速近似：UTF-8 中文 1 字 ≈ 3 byte。
// 不接 tokenizer，生产化通常换成 token 级估算。
func (c *Compactor) estimateLength(msgs []schema.Message) int {
	length := 0
	for _, msg := range msgs {
		length += len(msg.Content)
		for _, tc := range msg.ToolCalls {
			length += len(tc.Name) + len(tc.Arguments)
		}
	}
	return length
}
