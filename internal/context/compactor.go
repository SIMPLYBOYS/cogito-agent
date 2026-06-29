package context

import (
	"fmt"
	"log"
	"sync"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

const (
	defaultWatermark     = 0.75 // 上下文窗口填充率水位線：估算 token 達「窗口 × 水位」即壓縮
	defaultBytesPerToken = 3.0  // 首次校準前的初始位元組/Token 比（UTF-8 中文 ~3 byte/字 ≈ 1 token）
)

// Compactor 是上下文壓縮防線：在每次發 LLM 前，對"要發出去的窗口"按角色 × 位置 × 長度規則
// 壓縮。它只動發給 LLM 的副本，不損毀 session.history。
//
// 與滑動窗口的分工：窗口（GetWorkingMemory）防"歷史太長"（消息條數），Compactor 防"總量
// 逼近模型上下文上限"。
//
// 自適應壓縮：觸發水位不再是寫死的字符數，而是「模型真實上下文窗口（token）× 水位率」。
// 由於當前 context 還沒送出、無法預知其精確 token，Compactor 以位元組估算，並用每次 API 回傳的
// 真實 PromptTokens 自校準「位元組/Token 比」(Calibrate)，使估算隨真實 tokenizer 收斂——
// 從而自動適配不同窗口的模型（Claude 200k / Gemini 1M / 本地 Llama 8k）。
type Compactor struct {
	MaxContextTokens int     // 模型上下文窗口（token）；<=0 視為未知 → 不壓縮
	Watermark        float64 // 觸發壓縮的填充率（0~1）
	RetainLastMsgs   int     // 保護區大小（末 N 條不被嚴重清理）

	mu            sync.Mutex
	bytesPerToken float64 // 自校準的位元組/Token 比，隨真實 Usage 收斂
}

func NewCompactor(maxContextTokens int, watermark float64, retainLastMsgs int) *Compactor {
	if watermark <= 0 || watermark > 1 {
		watermark = defaultWatermark
	}
	return &Compactor{
		MaxContextTokens: maxContextTokens,
		Watermark:        watermark,
		RetainLastMsgs:   retainLastMsgs,
		bytesPerToken:    defaultBytesPerToken,
	}
}

// Calibrate 用一次真實 API 回傳的 PromptTokens 與當時送出的消息，更新位元組/Token 比。
// 注意：sentMsgs 僅含消息（不含 tools schema），而 promptTokens 含 tools 開銷，故估出的
// bytes/token 略偏小、傾向「提早一點壓縮」——這是安全方向。
func (c *Compactor) Calibrate(sentMsgs []schema.Message, promptTokens int) {
	if promptTokens <= 0 {
		return
	}
	sentBytes := c.estimateLength(sentMsgs)
	if sentBytes <= 0 {
		return
	}
	measured := float64(sentBytes) / float64(promptTokens)
	if measured < 1.0 { // 夾在合理區間，避免異常響應污染
		measured = 1.0
	}
	if measured > 10.0 {
		measured = 10.0
	}
	c.mu.Lock()
	// EWMA 平滑：隨真實 Usage 收斂，又不被單次抖動帶歪
	c.bytesPerToken = 0.5*c.bytesPerToken + 0.5*measured
	c.mu.Unlock()
}

// budgetTokens 是觸發壓縮的 token 水位（窗口 × 水位率）。窗口未知（<=0）時返回 0。
func (c *Compactor) budgetTokens() int {
	if c.MaxContextTokens <= 0 {
		return 0
	}
	return int(float64(c.MaxContextTokens) * c.Watermark)
}

// estimatedTokens 用自校準的位元組/Token 比，把當前消息的位元組數換算成 token 估算。
func (c *Compactor) estimatedTokens(msgs []schema.Message) int {
	c.mu.Lock()
	bpt := c.bytesPerToken
	c.mu.Unlock()
	if bpt <= 0 {
		bpt = defaultBytesPerToken
	}
	return int(float64(c.estimateLength(msgs)) / bpt)
}

func (c *Compactor) Compact(msgs []schema.Message) []schema.Message {
	budget := c.budgetTokens()
	if budget <= 0 {
		return msgs // 窗口未知 → 不壓縮（行為等同關閉）
	}

	currentTokens := c.estimatedTokens(msgs)
	if currentTokens < budget {
		return msgs
	}

	log.Printf("[Compactor] ⚠️ 上下文約 %d tokens 達到水位 %d（窗口 %d × %.0f%%），觸發自適應壓縮...\n",
		currentTokens, budget, c.MaxContextTokens, c.Watermark*100)

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
				// rune 安全：按【字元】切頭尾，避免切到中文多位元組字元中間產生非法 UTF-8（送進 LLM）。
				const headRunes, tailRunes = 500, 500
				if r := []rune(msg.Content); len(r) > headRunes+tailRunes {
					head := string(r[:headRunes])
					tail := string(r[len(r)-tailRunes:])
					newMsg.Content = fmt.Sprintf("%s\n\n...[內容過長，中間 %d 字已被系統截斷]...\n\n%s", head, len(r)-headRunes-tailRunes, tail)
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

	log.Printf("[Compactor] ✅ 壓縮完成。約 %d → %d tokens。\n", currentTokens, c.estimatedTokens(compacted))

	return compacted
}

// estimateLength 是位元組級的快速近似（UTF-8 中文 1 字 ≈ 3 byte），作為 token 估算的基底。
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
