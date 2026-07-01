package engine

import (
	"context"
	"fmt"
	"log"
	"strings"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

const (
	summaryTailMsgs    = 20  // 逐字保留的末 N 條（與 GetWorkingMemory 窗口對齊）
	summaryTriggerMsgs = 40  // history 超過此數才觸發逐出+摘要（tail + 緩衝，避免頻繁摘要）
	summaryMsgRuneCap  = 800 // 餵給摘要器時單條訊息最多取的字數（頭尾），避免摘要 prompt 爆量
)

const summarySystemPrompt = `你是對話壓縮器。把「先前摘要」與「新逐出的對話片段」合併成一份更新後的滾動摘要，供 agent 後續回顧早期脈絡。
規則（salience 優先——重要的留、瑣碎的丟）：
- 必須保留：使用者的目標與約束、明確決策與結論、使用者的更正/否決、尚未解決的問題、關鍵事實（路徑/名稱/數值/憑證位置）、踩過的錯誤與教訓。
- 可略去：例行的工具成功、寒暄、可從結果推得的中間步驟、重複內容。
- 用條列、繁體中文、精簡但保留可據以行動的細節。直接輸出更新後的摘要本身，不要任何前言或後語。`

// maintainSummary：history 過長時，把「超出逐字尾的舊訊息」摺進滾動摘要並從 history 真正逐出，
// 使 history 有界（[摘要] + 末 N 逐字）＝連貫性與記憶體同時收斂。salience 內建在 prompt。
// 只在超過門檻時付一次 LLM；摘要失敗則保留原歷史、下輪再試（不丟資料）。EnableSummary 關則直接跳過。
func (e *AgentEngine) maintainSummary(ctx context.Context, session *ctxpkg.Session) {
	if !e.EnableSummary {
		return
	}
	evicted := session.EvictablePrefix(summaryTailMsgs, summaryTriggerMsgs)
	if len(evicted) == 0 {
		return
	}
	newSummary, err := e.summarize(ctx, session.Summary(), evicted)
	if err != nil {
		log.Printf("[Summary] 摺疊失敗（保留原歷史、下輪再試）: %v", err)
		return
	}
	session.CommitSummary(newSummary, len(evicted))
	log.Printf("[Summary] 已把 %d 條舊訊息摺進滾動摘要，history 收斂至末 %d 條逐字。", len(evicted), summaryTailMsgs)
}

// summarize 用 LLM 把 prev 摘要與 evicted 片段增量合併成新摘要。
func (e *AgentEngine) summarize(ctx context.Context, prev string, evicted []schema.Message) (string, error) {
	var b strings.Builder
	if prev != "" {
		b.WriteString("## 先前摘要\n")
		b.WriteString(prev)
		b.WriteString("\n\n")
	}
	b.WriteString("## 新逐出的對話片段（依序）\n")
	b.WriteString(renderTranscript(evicted))

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: summarySystemPrompt},
		{Role: schema.RoleUser, Content: b.String()},
	}
	resp, err := e.provider.Generate(ctx, msgs, nil)
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return "", fmt.Errorf("摘要為空")
	}
	return out, nil
}

// renderTranscript 把訊息序列攤成精簡文字轉錄；超長內容頭尾截斷，避免摘要 prompt 爆量。
func renderTranscript(msgs []schema.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch {
		case m.Role == schema.RoleUser && m.ToolCallID != "":
			fmt.Fprintf(&b, "工具結果: %s\n", clampRunes(m.Content, summaryMsgRuneCap))
		case m.Role == schema.RoleUser:
			fmt.Fprintf(&b, "使用者: %s\n", clampRunes(m.Content, summaryMsgRuneCap))
		case m.Role == schema.RoleAssistant:
			if c := strings.TrimSpace(m.Content); c != "" {
				fmt.Fprintf(&b, "助手: %s\n", clampRunes(c, summaryMsgRuneCap))
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "助手呼叫工具: %s(%s)\n", tc.Name, clampRunes(string(tc.Arguments), 200))
			}
		}
	}
	return b.String()
}

// clampRunes 按【字元】截頭尾（rune 安全，不切壞中文），中間以標記省略。
func clampRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	head := limit * 3 / 4
	tail := limit - head
	return string(r[:head]) + " …[截斷]… " + string(r[len(r)-tail:])
}
