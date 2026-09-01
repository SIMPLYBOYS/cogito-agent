package tools

// 向外查證的兩顆工具（munder-difflin 對照之外的觀察修正：agent 不會主動補足輸入資訊，
// 因為工具清單裡根本沒有「向外查」這件事——模型的主動性基本上被工具清單定義）。
//
//   - web_search：Tavily 搜尋。回【結果清單】（標題/網址/摘錄），刻意不要 Tavily 的 AI answer——
//     紀律第 9 條只採一手來源，搜尋是【找到一手來源的路】，不是答案本身。
//   - fetch_url：抓單頁全文。走 Tavily /extract 而非本機 HTTP GET——抓取發生在 Tavily 那端，
//     agent 拿不到 localhost/內網（把 SSRF 這整類問題結構性排除，而不是靠網址黑名單）。
//
// TAVILY_API_KEY 未設＝兩顆都不註冊（agentkit），與其他可選能力同款靜默降級。
// 查詢/網址會外送 Tavily：查詢封頂防大段內文被夾帶外滲，機密片段掃描在審批層
// （IsDangerousCommand 對這兩顆工具跑 secretSegments，與 bash 同一道）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

const (
	tavilyBase      = "https://api.tavily.com"
	webQueryMax     = 400  // 查詢封頂（rune）：查詢該是問題，不是一整段被夾帶出去的內文
	webSnippetMax   = 600  // 每筆結果摘錄封頂
	webPageMax      = 8000 // fetch_url 全文封頂
	webResultsCap   = 8
	webResultsByDef = 5
)

// tavilyPost 打一個 Tavily 端點。key 同時放 body 與 Bearer（新舊版 API 各認一種）。
func tavilyPost(ctx context.Context, base, key, path string, body map[string]any) ([]byte, error) {
	body["api_key"] = key
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("連不上搜尋服務: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("搜尋服務回 %d: %s", resp.StatusCode, schema.TruncRunes(string(out), 200, "…"))
	}
	return out, nil
}

// WebSearchTool 上網搜尋（Tavily）。
type WebSearchTool struct {
	key  string
	base string // 測試注入用；正式一律 tavilyBase
}

func NewWebSearchTool(key string) *WebSearchTool { return &WebSearchTool{key: key, base: tavilyBase} }

func (t *WebSearchTool) Name() string { return "web_search" }

func (t *WebSearchTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "上網搜尋。輸入資訊不足、或涉及你訓練資料之後的事實（新版本、新聞、價格、規格）時用它查證，" +
			"不要憑印象作答。回傳結果清單（標題/網址/摘錄）——摘錄是二手線索，重要說法請再用 fetch_url 打開原頁確認一手來源。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "description": "搜尋語句（具體的問題或關鍵字組合）"},
				"max_results": map[string]any{"type": "integer", "description": fmt.Sprintf("回傳筆數 1–%d（預設 %d）", webResultsCap, webResultsByDef)},
			},
			"required": []string{"query"},
		},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("需要 query（搜尋語句）")
	}
	n := in.MaxResults
	if n < 1 || n > webResultsCap {
		n = webResultsByDef
	}
	raw, err := tavilyPost(ctx, t.base, t.key, "/search", map[string]any{
		"query":        schema.TruncRunes(strings.TrimSpace(in.Query), webQueryMax, "…"),
		"max_results":  n,
		"search_depth": "basic",
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("搜尋回應解析失敗: %w", err)
	}
	if len(out.Results) == 0 {
		return "（沒有搜尋結果——換個關鍵字，或這件事網路上真的查不到）", nil
	}
	var b strings.Builder
	for i, r := range out.Results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL,
			schema.TruncRunes(strings.TrimSpace(r.Content), webSnippetMax, "…"))
	}
	b.WriteString("（摘錄是二手線索；要引用的說法請用 fetch_url 打開原頁確認）")
	return b.String(), nil
}

// FetchURLTool 抓單頁全文（Tavily /extract；抓取發生在遠端，拿不到本機/內網位址）。
type FetchURLTool struct {
	key  string
	base string
}

func NewFetchURLTool(key string) *FetchURLTool { return &FetchURLTool{key: key, base: tavilyBase} }

func (t *FetchURLTool) Name() string { return "fetch_url" }

func (t *FetchURLTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "抓取一個公開網頁的正文（純文字）。搭配 web_search：搜尋給線索、這顆讀一手來源。" +
			"只能抓公開網址（抓取由搜尋服務代抓，內網位址拿不到）。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "要抓的網址（http/https）"},
			},
			"required": []string{"url"},
		},
	}
}

func (t *FetchURLTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.URL) == "" {
		return "", fmt.Errorf("需要 url")
	}
	u := strings.TrimSpace(in.URL)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "", fmt.Errorf("只吃 http/https 網址")
	}
	raw, err := tavilyPost(ctx, t.base, t.key, "/extract", map[string]any{"urls": []string{u}})
	if err != nil {
		return "", err
	}
	var out struct {
		Results []struct {
			URL        string `json:"url"`
			RawContent string `json:"raw_content"`
		} `json:"results"`
		Failed []struct {
			URL   string `json:"url"`
			Error string `json:"error"`
		} `json:"failed_results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("抓頁回應解析失敗: %w", err)
	}
	if len(out.Results) == 0 {
		reason := "無內容"
		if len(out.Failed) > 0 && out.Failed[0].Error != "" {
			reason = out.Failed[0].Error
		}
		return "", fmt.Errorf("抓不到這一頁（%s）", schema.TruncRunes(reason, 120, "…"))
	}
	return schema.TruncRunes(strings.TrimSpace(out.Results[0].RawContent), webPageMax, "…〔全文更長，已截斷〕"), nil
}
