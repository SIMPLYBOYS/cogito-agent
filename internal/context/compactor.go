package context

import (
	"fmt"
	"log"

	"github.com/yourname/go-tiny-claw/internal/schema"
)

// Compactor 是字符級的上下文壓縮防線：在每次發 LLM 前，對"要發出去的窗口"按
// 角色 × 位置 × 長度 三維規則壓縮。它只動發給 LLM 的副本，不損毀 session.history。
//
// 與滑動窗口的分工：窗口（GetWorkingMemory）防"歷史太長"（消息條數），
// Compactor 防"單條消息太大"（字符數，OOM 元兇通常是早期讀進來的大文件/巨型日誌）。
type Compactor struct {
	MaxChars       int // 壓縮觸發閾值（總字符數）
	RetainLastMsgs int // 保護區大小（末 N 條不被嚴重清理）
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

	log.Printf("[Compactor] ⚠️ 內存告警：當前上下文長度 (%d 字符) 超過閾值 (%d)，觸發壓縮清理...\n", currentLength, c.MaxChars)

	var compacted []schema.Message
	msgCount := len(msgs)

	protectStartIndex := msgCount - c.RetainLastMsgs
	if protectStartIndex < 0 {
		protectStartIndex = 0
	}

	for i, msg := range msgs {
		if msg.Role == schema.RoleSystem {
			compacted = append(compacted, msg) // system 永遠完整
			continue
		}

		newMsg := msg
		isInWorkingMemory := i >= protectStartIndex

		if msg.Role == schema.RoleUser && msg.ToolCallID != "" {
			// tool_result 是最大威脅：早期的整段替換為佔位符，近期的頭尾保留
			if !isInWorkingMemory {
				if len(msg.Content) > 200 {
					newMsg.Content = fmt.Sprintf("...[為了節省內存，早期的工具輸出已被系統強制清理。原始長度: %d 字節]...", len(msg.Content))
				}
			} else {
				const maxKeep = 1000
				if len(msg.Content) > maxKeep {
					head := msg.Content[:500]
					tail := msg.Content[len(msg.Content)-500:]
					newMsg.Content = fmt.Sprintf("%s\n\n...[內容過長，中間 %d 字節已被系統截斷]...\n\n%s", head, len(msg.Content)-maxKeep, tail)
				}
			}
		} else if msg.Role == schema.RoleAssistant && msg.Content != "" {
			// 早期的 assistant 思考摺疊為一句話；近期的完整保留。
			// 注意：不動 ToolCalls —— tool_use 結構必須與後續 tool_result 配對。
			if !isInWorkingMemory && len(msg.Content) > 200 {
				newMsg.Content = "...[早期的推理思考過程已摺疊]..."
			}
		}

		compacted = append(compacted, newMsg)
	}

	newLength := c.estimateLength(compacted)
	log.Printf("[Compactor] ✅ 壓縮完成。上下文長度從 %d 降至 %d 字符。\n", currentLength, newLength)

	return compacted
}

// estimateLength 是字符級（按 byte）的快速近似：UTF-8 中文 1 字 ≈ 3 byte。
// 不接 tokenizer，生產化通常換成 token 級估算。
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
