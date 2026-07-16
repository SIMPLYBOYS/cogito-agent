package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxResponseBytes 是單次 JSON 回應的讀取上限。正常 MCP 回應（含大型 tools/list）遠小於此；
// 設上限是因為 httpClient 刻意不設 client 級 timeout（會殺掉合法的長 SSE 串流），故無限 body
// 沒有別的東西擋得住——一個故障或惡意的 server 就能把整個 bot 進程 OOM 掉。
const maxResponseBytes = 32 << 20 // 32 MiB

// httpTransport 走 MCP 的 Streamable HTTP 傳輸（spec 2025-03-26+）：每個 JSON-RPC 訊息是一個 POST，
// 回應可能是單個 application/json，或一段 text/event-stream（SSE）。session 以 Mcp-Session-Id 維持，
// 後續請求帶上協商出的 MCP-Protocol-Version。只做 client→server 的請求/通知（不開 GET 長串流）。
type httpTransport struct {
	name       string
	url        string
	headers    map[string]string
	httpClient *http.Client

	mu                sync.Mutex
	nextID            int
	sessionID         string // 來自 initialize 回應的 Mcp-Session-Id（之後每次請求帶上）
	negotiatedVersion string // initialize 協商出的協定版本（送 MCP-Protocol-Version 用）
}

func newHTTPTransport(cfg ServerConfig) *httpTransport {
	return &httpTransport{
		name:       cfg.Name,
		url:        cfg.URL,
		headers:    cfg.Headers,
		httpClient: &http.Client{}, // 不設 client 級 timeout，逾時交給每次呼叫的 ctx 控制
	}
}

func dialHTTP(ctx context.Context, cfg ServerConfig) (*Client, error) {
	c := &Client{Name: cfg.Name, t: newHTTPTransport(cfg)}
	if err := c.initialize(ctx); err != nil {
		return nil, fmt.Errorf("mcp[%s]: 握手失敗: %w", cfg.Name, err)
	}
	return c, nil
}

// doPost 送一個 JSON-RPC payload 到端點，回傳 http 回應（caller 負責讀/關 body）。
func (h *httpTransport) doPost(ctx context.Context, payload any) (*http.Response, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream") // spec 要求兩種都列
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	h.mu.Lock()
	sid, ver := h.sessionID, h.negotiatedVersion
	h.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	// 只在 initialize 協商出版本後才帶（spec：此頭用於 initialize 之後的請求）。
	if ver != "" {
		req.Header.Set("MCP-Protocol-Version", ver)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp[%s]: HTTP 請求失敗: %w", h.name, err)
	}
	// 伺服器可能在 initialize 回應頭給 session id → 記下,後續請求帶上。
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		h.mu.Lock()
		h.sessionID = got
		h.mu.Unlock()
	}
	return resp, nil
}

func (h *httpTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	h.mu.Unlock()

	resp, err := h.doPost(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// session 過期：清掉，下次請求不帶舊 id（伺服器多會當新 session 處理）。
		h.mu.Lock()
		h.sessionID = ""
		h.mu.Unlock()
		return nil, fmt.Errorf("mcp[%s]: session 已過期 (HTTP 404)，已清除，請重試", h.name)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("mcp[%s] %s: HTTP %d: %s", h.name, method, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rr rpcResponse
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		rr, err = readSSEResponse(resp.Body, id)
		if err != nil {
			return nil, fmt.Errorf("mcp[%s] %s: %w", h.name, method, err)
		}
	} else {
		// 上限：擋故障/惡意 server 回無限 body 打爆記憶體（配上無 client timeout，bot 無法自救）。
		// 錯誤路徑（上面）本來就有 LimitReader(2048)，成功路徑卻沒有——保護不對稱是漏的，不是有意的。
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil {
			return nil, fmt.Errorf("mcp[%s] %s: 讀取回應失敗: %w", h.name, method, err)
		}
		if len(body) > maxResponseBytes {
			return nil, fmt.Errorf("mcp[%s] %s: 回應超過 %d MiB 上限，已中止（server 故障或惡意？）",
				h.name, method, maxResponseBytes>>20)
		}
		if err := json.Unmarshal(body, &rr); err != nil {
			return nil, fmt.Errorf("mcp[%s] %s: 解析 JSON 回應失敗: %w", h.name, method, err)
		}
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("mcp[%s] %s 錯誤 %d: %s", h.name, method, rr.Error.Code, rr.Error.Message)
	}

	// initialize：記下協商版本,供後續 MCP-Protocol-Version 頭。
	if method == "initialize" && len(rr.Result) > 0 {
		var ir struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(rr.Result, &ir) == nil && ir.ProtocolVersion != "" {
			h.mu.Lock()
			h.negotiatedVersion = ir.ProtocolVersion
			h.mu.Unlock()
		}
	}
	return rr.Result, nil
}

func (h *httpTransport) notify(ctx context.Context, method string, params any) error {
	resp, err := h.doPost(ctx, rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	// spec：純通知/回應的 POST,伺服器回 202 Accepted、無 body。容忍其他 2xx。
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("mcp[%s]: 通知 %s 回 HTTP %d", h.name, method, resp.StatusCode)
	}
	return nil
}

func (h *httpTransport) close() error {
	// 盡力而為地終止 session（spec：DELETE + Mcp-Session-Id）；伺服器可回 405 表示不支援,忽略。
	h.mu.Lock()
	sid := h.sessionID
	h.mu.Unlock()
	if sid == "" {
		return nil
	}
	// 帶 timeout，避免關機路徑在無回應的 DELETE 上永久阻塞（httpClient 本身刻意無 timeout）。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, h.url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Mcp-Session-Id", sid)
	if ver := h.negotiatedVersion; ver != "" {
		req.Header.Set("MCP-Protocol-Version", ver)
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	if resp, err := h.httpClient.Do(req); err == nil {
		_ = resp.Body.Close()
	}
	return nil
}

// readSSEResponse 從 SSE 串流讀到 id 相符的 JSON-RPC 回應。串流中可能先夾帶 server→client 的
// 請求/通知（id 不符或無 id），略過;讀到相符回應即返回。
func readSSEResponse(r io.Reader, wantID int) (rpcResponse, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var data strings.Builder
	dispatch := func() (rpcResponse, bool) {
		if data.Len() == 0 {
			return rpcResponse{}, false
		}
		payload := data.String()
		data.Reset()
		var rr rpcResponse
		if json.Unmarshal([]byte(payload), &rr) == nil && idMatches(rr.ID, wantID) {
			return rr, true
		}
		return rpcResponse{}, false
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" { // 事件邊界
			if rr, ok := dispatch(); ok {
				return rr, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		// 忽略 event: / id: / 註解行
	}
	if rr, ok := dispatch(); ok { // 末事件無結尾空行的情況
		return rr, nil
	}
	if err := sc.Err(); err != nil {
		return rpcResponse{}, fmt.Errorf("讀 SSE 串流失敗: %w", err)
	}
	return rpcResponse{}, fmt.Errorf("SSE 串流結束仍未收到 id=%d 的回應", wantID)
}
