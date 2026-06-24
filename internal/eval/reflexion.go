package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// injectLessons 把前幾次失敗反思出的「教訓」併進重試的任務指令——這就是 Reflexion 的「回注」。
func injectLessons(taskPrompt string, lessons []string) string {
	if len(lessons) == 0 {
		return taskPrompt
	}
	var b strings.Builder
	b.WriteString(taskPrompt)
	b.WriteString("\n\n【前次嘗試失敗的教訓，務必避免重蹈】\n")
	for i, l := range lessons {
		fmt.Fprintf(&b, "%d. %s\n", i+1, l)
	}
	return b.String()
}

const reflexionSystemPrompt = `你是負責「失敗反思」的教練。一個 agent 剛嘗試完成任務但【驗證失敗】。
看完任務、執行軌跡、與失敗訊息後，產出一條【具體、可操作】的教訓，讓它下次重試時避開同一個錯。
- 聚焦「下次該怎麼做不同」，不要只複述發生了什麼。
- 一到三句，祈使句；不要寫死本次的具體數值。
只輸出 JSON：{"lesson": "<教訓>"}。`

// ReflectOnFailure 對一次失敗的嘗試反思，產出一條可回注的教訓。空字串表示沒給出有效教訓。
func ReflectOnFailure(ctx context.Context, p provider.LLMProvider, taskPrompt string, history []schema.Message, failureOutput string) (string, error) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: reflexionSystemPrompt},
		{Role: schema.RoleUser, Content: fmt.Sprintf("任務：\n%s\n\n執行軌跡：\n%s\n\n失敗訊息：\n%s",
			taskPrompt, renderTranscript(history, 5000), oneLine(failureOutput, 800))},
	}
	resp, err := p.Generate(ctx, msgs, nil)
	if err != nil {
		return "", fmt.Errorf("反思 LLM 調用失敗: %w", err)
	}
	var out struct {
		Lesson string `json:"lesson"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); err != nil {
		return "", fmt.Errorf("反思輸出非合法 JSON（%q）: %w", resp.Content, err)
	}
	return strings.TrimSpace(out.Lesson), nil
}

// renderTranscript 把對話歷史壓成精簡軌跡（上限 maxChars）。
func renderTranscript(history []schema.Message, maxChars int) string {
	var b strings.Builder
	for _, m := range history {
		if m.Role == schema.RoleSystem {
			continue
		}
		b.WriteString(string(m.Role) + ": " + oneLine(m.Content, 300))
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, " [呼叫 %s %s]", tc.Name, oneLine(string(tc.Arguments), 200))
		}
		b.WriteString("\n")
	}
	s := b.String()
	if len(s) > maxChars {
		return s[:maxChars] + "\n...[軌跡過長已截斷]"
	}
	return s
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	if len([]rune(s)) > max {
		return string([]rune(s)[:max]) + "…"
	}
	return s
}

func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return strings.TrimSpace(s)
}
