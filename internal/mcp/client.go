// Package mcp 是一個極簡的 Model Context Protocol 客戶端：以 JSON-RPC 2.0 連接外部 MCP 工具
// 伺服器，做 initialize → tools/list → tools/call。v1 只支援 tools（不含 resources/prompts）。
// 傳輸有兩種：stdio（子行程，stdio_transport）與 Streamable HTTP（遠端，http_transport）。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
)

const protocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"` // JSON-RPC id 可為數字或字串；用 RawMessage 容錯比對
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// parseID 把 JSON-RPC 回應的 id（數字或字串型）正規化成 int。我們送出的 id 一律是遞增 int，
// 但某些伺服器會回成字串（如 "5"）；兩種都接。空/null/非數字 → ok=false（通知或無 id）。
func parseID(raw json.RawMessage) (int, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	s = strings.Trim(s, `"`) // 容忍字串型 id
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// idMatches 判斷回應 id 是否等於我們送出的請求 id（容忍數字/字串型）。
func idMatches(raw json.RawMessage, want int) bool {
	id, ok := parseID(raw)
	return ok && id == want
}

// transport 抽象 JSON-RPC 的一次往返與通知傳送，讓 stdio / HTTP 共用上層的 initialize/list/call 邏輯。
type transport interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	close() error
}

// Client 是單一 MCP 伺服器的連線（傳輸無關的外殼）。上層 initialize/listTools/callTool 走 transport。
type Client struct {
	Name string
	// Instructions 是 server 在 initialize 握手回傳的【使用指引】（可選、可能很長）。承載「這些工具
	// 該怎麼用」的脈絡（如查詢語法、常見流程）。經 gateway 的 mcp_describe_tool 按需提供給 agent。
	Instructions string
	t            transport
}

// newClient 以 stdio 傳輸建立 Client（傳入已接好的讀寫端）；供測試以 in-process 假 server 注入。
func newClient(name string, w io.Writer, r io.Reader, cmd *exec.Cmd) *Client {
	return &Client{Name: name, t: newStdioTransport(name, w, r, cmd)}
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.t.call(ctx, method, params)
}

// initialize 完成 MCP 握手：initialize 請求 + notifications/initialized 通知。
func (c *Client) initialize(ctx context.Context) error {
	raw, err := c.t.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "cogito-agent", "version": "0.1.0"},
	})
	if err != nil {
		return err
	}
	// 抓 server 級使用指引（可選）：供 gateway 按需（describe 時）餵給 agent。解析失敗不致命。
	var res struct {
		Instructions string `json:"instructions"`
	}
	if json.Unmarshal(raw, &res) == nil {
		c.Instructions = strings.TrimSpace(res.Instructions)
	}
	return c.t.notify(ctx, "notifications/initialized", nil)
}

// toolSpec 是 tools/list 回傳的單個工具定義。
type toolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

func (c *Client) listTools(ctx context.Context) ([]toolSpec, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []toolSpec `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp[%s]: 解析 tools/list 失敗: %w", c.Name, err)
	}
	return out.Tools, nil
}

// contentBlock 是 tools/call 結果裡的內容塊（v1 只取 text）。
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// callTool 呼叫遠端工具，把回傳的 text 內容塊合併成字串；isError=true 時以 error 回傳
// （交給 Registry 走 error-as-observation）。
func (c *Client) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var out struct {
		Content []contentBlock `json:"content"`
		IsError bool           `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("mcp[%s]: 解析 tools/call 結果失敗: %w", c.Name, err)
	}
	var text string
	for _, b := range out.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	if out.IsError {
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}

// Close 關閉連線（結束子行程 / 釋放 HTTP 資源）。
func (c *Client) Close() error { return c.t.close() }

// Dial 依設定建立連線並完成握手：有 url/type=http 走 Streamable HTTP，否則 stdio 子行程。
func Dial(ctx context.Context, cfg ServerConfig) (*Client, error) {
	if cfg.isHTTP() {
		return dialHTTP(ctx, cfg)
	}
	return dialStdio(ctx, cfg)
}

func dialStdio(ctx context.Context, cfg ServerConfig) (*Client, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	// 【為何不用 cmd.Environ()】那會把本行程【全部】環境變數交出去——包含 ANTHROPIC_API_KEY、
	// bot token。MCP server 多半是 npx/uvx 拉下來的第三方套件，等於把所有金鑰交給不是自己寫的
	// 程式碼（供應鏈曝險）。改成白名單基底 + .mcp.json 明確宣告的 env：server 真正需要的憑證
	// 由設定明講，其餘一律不給。
	cmd.Env = append(sandbox.FilteredEnv(), cfg.envSlice()...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp[%s]: 啟動失敗: %w", cfg.Name, err)
	}
	// 子行程 stderr 轉印到日誌，避免緩衝塞滿阻塞。
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Printf("[mcp:%s] %s", cfg.Name, sc.Text())
		}
	}()

	c := newClient(cfg.Name, stdin, stdout, cmd)
	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp[%s]: 握手失敗: %w", cfg.Name, err)
	}
	return c, nil
}

// ---- stdio 傳輸 ----

// stdioTransport 走子行程 stdin/stdout（換行分隔 JSON）。背景 reader 讀回應、按 id 路由到等待中的
// 呼叫，因此可安全併發。
type stdioTransport struct {
	name string
	w    io.Writer
	cmd  *exec.Cmd

	mu      sync.Mutex // 保護 pending / nextID / closed
	nextID  int
	pending map[int]chan rpcResponse
	closed  bool
	closeCh chan struct{}

	writeMu sync.Mutex // 序列化寫入；與 mu 分開，避免阻塞寫入時擋住 readLoop 路由（死鎖）
}

func newStdioTransport(name string, w io.Writer, r io.Reader, cmd *exec.Cmd) *stdioTransport {
	t := &stdioTransport{
		name:    name,
		w:       w,
		cmd:     cmd,
		pending: make(map[int]chan rpcResponse),
		closeCh: make(chan struct{}),
	}
	go t.readLoop(r)
	return t
}

func (t *stdioTransport) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // 容納大型工具輸出
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if json.Unmarshal(line, &resp) != nil {
			continue
		}
		id, hasID := parseID(resp.ID)
		if !hasID {
			continue // 非回應（通知/無 id/解析失敗）→ v1 忽略
		}
		t.mu.Lock()
		ch, ok := t.pending[id]
		delete(t.pending, id)
		t.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
	// 讀取結束（EOF / 行程退出）：喚醒所有等待者並標記關閉。
	t.mu.Lock()
	t.closed = true
	for id, ch := range t.pending {
		close(ch)
		delete(t.pending, id)
	}
	t.mu.Unlock()
	close(t.closeCh)
}

func (t *stdioTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, fmt.Errorf("mcp[%s]: 連線已關閉", t.name)
	}
	t.nextID++
	id := t.nextID
	ch := make(chan rpcResponse, 1)
	t.pending[id] = ch
	t.mu.Unlock()

	if err := t.write(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mcp[%s]: 連線在等待 %s 回應時關閉", t.name, method)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp[%s] %s 錯誤 %d: %s", t.name, method, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (t *stdioTransport) notify(_ context.Context, method string, params any) error {
	return t.write(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (t *stdioTransport) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n') // stdio 傳輸：換行分隔，訊息內不得含換行
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err = t.w.Write(b)
	return err
}

func (t *stdioTransport) close() error {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
	}
	return nil
}
