package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// SearchSessionsTool 讓 agent 檢索【過去的對話】。先前沒有這個入口——retrospect 之類的技能只能
// 叫 agent 自己 ls sessions 目錄、grep JSON、再自行拼湊，既不穩定又把整份 JSON 讀進 context。
// 這裡回傳的是有界的命中摘要（session id + 時間 + 成本/輪數 + 幾段命中片段），要細節再自己讀檔。
type SearchSessionsTool struct {
	store  ctxpkg.SessionStore
	selfID string // 呼叫者自己的 session id：預設排除（覆盤自己的覆盤沒有意義）
}

// NewSearchSessionsTool 的 store 可為 nil（未設 COGITO_SESSION_DIR 的純記憶體模式）——
// 此時工具仍註冊，但會明確回報「未啟用持久化」，而不是假裝查無結果。
func NewSearchSessionsTool(store ctxpkg.SessionStore, selfSessionID string) *SearchSessionsTool {
	return &SearchSessionsTool{store: store, selfID: selfSessionID}
}

func (t *SearchSessionsTool) Name() string { return "search_sessions" }

func (t *SearchSessionsTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "依關鍵字檢索【過去的對話紀錄】（跨 session、跨頻道），回傳最相關的幾段對話摘要——" +
			"何時、哪個會話、花了多少、命中片段。需要回想「這件事以前處理過嗎／上次怎麼解的／使用者之前說過什麼」" +
			"時呼叫（支援中英關鍵字）。這比自己 grep session JSON 準確且省 context。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "檢索關鍵字，可多詞（中英皆可）",
				},
				"days": map[string]interface{}{
					"type":        "integer",
					"description": "（可選）只看近 N 天；省略＝不限時間",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "（可選）最多回傳幾個 session，預設 5、上限 20",
				},
			},
			"required": []string{"query"},
		},
	}
}

type searchSessionsArgs struct {
	Query string `json:"query"`
	Days  int    `json:"days"`
	Limit int    `json:"limit"`
}

func (t *SearchSessionsTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in searchSessionsArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("解析參數失敗: %w", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return "請提供檢索關鍵字（query）。", nil
	}
	if t.store == nil {
		return "未啟用 session 持久化（COGITO_SESSION_DIR 未設），沒有過去的對話可檢索。", nil
	}
	hits, err := ctxpkg.SearchSessions(t.store, ctxpkg.SessionSearchOpts{
		Query: in.Query, Days: in.Days, Limit: in.Limit, Exclude: t.selfID,
	})
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("過去的對話中找不到與「%s」相關的內容。", in.Query), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "在過去的對話中找到 %d 個相關會話（依相關度排序）：\n", len(hits))
	for _, h := range hits {
		fmt.Fprintf(&b, "\n## %s（%s）\n", h.ID, h.UpdatedAt)
		fmt.Fprintf(&b, "- %d 則訊息 · $%.4f", h.Turns, h.CostUSD)
		if h.Goal != "" {
			fmt.Fprintf(&b, " · 目標：%s", schema.TruncRunes(h.Goal, 60, "…"))
		}
		b.WriteString("\n")
		for _, s := range h.Snippets {
			fmt.Fprintf(&b, "  %s\n", s)
		}
	}
	b.WriteString("\n（以上為片段摘要；需要完整脈絡可用 read_file 讀該 session 的 JSON。）")
	return b.String(), nil
}
