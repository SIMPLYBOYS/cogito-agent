package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

const goalJudgeSystem = `你是嚴格的目標驗收者。依「驗收標準」判斷「agent 的產出」是否真的達成——寬鬆會害了使用者，沒達成就明說沒達成並指出缺口。只輸出一個 JSON 物件，不要任何其他文字或 markdown 圍欄：{"done": true|false, "reason": "<一句話依據或缺口>"}`

// JudgeGoal 用 LLM 依 criteria 判定本會話最後的產出是否達成目標——給 goal 迴圈驗收 bash 難驗的任務
// （寫文件 / 設計 / 內容類）。回傳 (達成?, 理由)。判定成本走 e.provider（在 CLI 已被 CostTracker 包裹）。
func (e *AgentEngine) JudgeGoal(ctx context.Context, session *ctxpkg.Session, criteria string) (bool, string, error) {
	last := lastAssistantContent(session)
	if strings.TrimSpace(last) == "" {
		return false, "agent 尚無產出可驗收", nil
	}
	user := fmt.Sprintf("驗收標準：\n%s\n\nagent 的產出：\n%s", criteria, clampRunes(last, 4000))
	resp, err := e.provider.Generate(ctx, []schema.Message{
		{Role: schema.RoleSystem, Content: goalJudgeSystem},
		{Role: schema.RoleUser, Content: user},
	}, nil)
	if err != nil {
		return false, "", fmt.Errorf("驗收 LLM 調用失敗: %w", err)
	}
	var r struct {
		Done   bool   `json:"done"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(resp.Content)), &r); err != nil {
		return false, "", fmt.Errorf("驗收輸出非合法 JSON（%q）: %w", resp.Content, err)
	}
	return r.Done, r.Reason, nil
}

// lastAssistantContent 取本會話最後一則有內容的助手訊息。
func lastAssistantContent(session *ctxpkg.Session) string {
	h := session.GetWorkingMemory(0)
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Role == schema.RoleAssistant {
			if c := strings.TrimSpace(h[i].Content); c != "" {
				return c
			}
		}
	}
	return ""
}

// extractJSONObject 從可能夾雜前後語/圍欄的字串裡，用括號配對取第一個平衡的 {...} JSON 物件。
func extractJSONObject(s string) string {
	i := strings.IndexByte(s, '{')
	if i < 0 {
		return s
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[i : j+1]
			}
		}
	}
	return s[i:]
}
