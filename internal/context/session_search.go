package context

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// 過去的對話是 agent 最大的一份資料，卻一直沒有檢索入口——retrospect 之類的技能只能叫 agent
// 自己 `ls` sessions 目錄、`grep` JSON、再自行拼湊，既不穩定又把整份 JSON 讀進 context 燒 token。
// 這裡給一個確定性的檢索：與 recall 共用同一套詞法（英數整詞 + CJK bigram），輸出【有界】。
//
// ponytail: 線性掃描 + 關鍵字評分。session 數量級是「幾十到幾百」，掃一遍是毫秒級；真到需要
// 索引（上萬 session）再上 SQLite FTS——介面不變，只換這支的實作。

const (
	maxSessionSnippets  = 3   // 每個 session 最多幾段命中片段
	sessionSnippetRunes = 160 // 每段片段長度上限
	defaultSessionLimit = 5   // 預設回傳幾個 session
	maxSessionLimit     = 20  // 硬上限——結果會進 agent context，不能無限長
	snippetContextRunes = 60  // 命中詞前後各留多少字元
)

// SessionHit 是一次 session 檢索的命中。
type SessionHit struct {
	ID        string
	UpdatedAt string
	Score     int
	CostUSD   float64
	Turns     int      // history 訊息數（多＝那次任務跑得久，通常更值得看）
	Goal      string   // 有持久目標的 session 值得特別看
	Snippets  []string // 命中片段（已截斷、已標註角色）
}

// SessionSearchOpts 是檢索條件。零值即可用（全部 session、預設筆數）。
type SessionSearchOpts struct {
	Query   string
	Days    int    // 只看近 N 天（依 UpdatedAt）；<=0＝不限
	Limit   int    // 回傳幾個 session；<=0＝預設 5，上限 20
	Exclude string // 要跳過的 session id——通常是呼叫者自己（覆盤自己的覆盤沒有意義）
}

// SearchSessions 對落地的 session 做關鍵字檢索，依相關度回傳有界的命中清單。
// store 為 nil（純記憶體模式、沒設 COGITO_SESSION_DIR）時回空——呼叫端據此提示使用者。
func SearchSessions(store SessionStore, opts SessionSearchOpts) ([]SessionHit, error) {
	if store == nil {
		return nil, nil
	}
	terms := tokenize(opts.Query)
	if len(terms) == 0 {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultSessionLimit
	}
	if limit > maxSessionLimit {
		limit = maxSessionLimit
	}
	var cutoff time.Time
	if opts.Days > 0 {
		cutoff = time.Now().AddDate(0, 0, -opts.Days)
	}

	ids, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("列出 session 失敗: %w", err)
	}

	var hits []SessionHit
	for _, id := range ids {
		if id == opts.Exclude {
			continue
		}
		snap, ok, err := store.Load(id)
		if err != nil || !ok {
			continue // 壞檔/剛被刪：跳過，不讓一個壞檔擋掉整次檢索
		}
		if !cutoff.IsZero() {
			t, perr := time.Parse(time.RFC3339, snap.UpdatedAt)
			if perr != nil || t.Before(cutoff) {
				continue
			}
		}
		if h, matched := scoreSession(snap, terms); matched {
			hits = append(hits, h)
		}
	}

	// 分數高的優先；同分則新的優先（近期的通常更相關）。
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].UpdatedAt > hits[j].UpdatedAt
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// scoreSession 算一個 session 的相關度並抽出命中片段。matched=false 表示完全沒命中。
func scoreSession(snap *SessionSnapshot, terms []string) (SessionHit, bool) {
	h := SessionHit{
		ID: snap.ID, UpdatedAt: snap.UpdatedAt,
		CostUSD: snap.TotalCostUSD, Turns: len(snap.History), Goal: snap.Goal,
	}
	for _, m := range snap.History {
		if m.Content == "" {
			continue
		}
		low := strings.ToLower(m.Content)
		hitHere := 0
		for _, t := range terms {
			hitHere += strings.Count(low, t)
		}
		if hitHere == 0 {
			continue
		}
		h.Score += hitHere
		if len(h.Snippets) < maxSessionSnippets {
			h.Snippets = append(h.Snippets, snippetAround(m, low, terms))
		}
	}
	return h, h.Score > 0
}

// snippetAround 從命中的訊息裡切出「命中詞前後一小段」，並標註角色——直接回傳整則訊息會把
// 工具輸出（動輒數千字）灌進 context，那正是這個工具要取代的行為。
func snippetAround(m schema.Message, lowered string, terms []string) string {
	runes := []rune(m.Content)
	at := -1
	for _, t := range terms {
		if i := strings.Index(lowered, t); i >= 0 {
			at = len([]rune(lowered[:i])) // byte offset → rune offset
			break
		}
	}
	start := 0
	if at > snippetContextRunes {
		start = at - snippetContextRunes
	}
	end := start + sessionSnippetRunes
	if end > len(runes) {
		end = len(runes)
	}
	body := strings.TrimSpace(strings.ReplaceAll(string(runes[start:end]), "\n", " "))
	if start > 0 {
		body = "…" + body
	}
	if end < len(runes) {
		body += "…"
	}
	role := string(m.Role)
	if m.ToolCallID != "" {
		role = "tool" // 工具結果在 schema 上是 user+ToolCallID，標成 tool 讀者才看得懂
	}
	return "[" + role + "] " + body
}
