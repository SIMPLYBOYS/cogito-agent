// Package mcp 是一個極簡的 Model Context Protocol 客戶端：以 JSON-RPC 2.0 over stdio
// （換行分隔的 JSON）連接外部 MCP 工具伺服器，做 initialize → tools/list → tools/call。
// v1 只支援 tools（不含 resources/prompts），傳輸只支援 stdio（子進程）。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
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
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// Client 是單一 MCP 伺服器的連線。背景 reader 讀取回應、按 id 路由到等待中的呼叫，
// 因此可安全地併發呼叫（我們的工具是並行執行的）。
type Client struct {
	Name string

	w   io.Writer
	cmd *exec.Cmd

	mu      sync.Mutex // 保護 pending / nextID / closed
	nextID  int
	pending map[int]chan rpcResponse
	closed  bool
	closeCh chan struct{}

	writeMu sync.Mutex // 序列化寫入；與 mu 分開，避免阻塞寫入時擋住 readLoop 路由（死鎖）
}

// newClient 是低階建構子（傳入已接好的讀寫端），供測試以 in-process 假 server 注入。
func newClient(name string, w io.Writer, r io.Reader, cmd *exec.Cmd) *Client {
	c := &Client{
		Name:    name,
		w:       w,
		cmd:     cmd,
		pending: make(map[int]chan rpcResponse),
		closeCh: make(chan struct{}),
	}
	go c.readLoop(r)
	return c
}

func (c *Client) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // 容納大型工具輸出
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil || resp.ID == 0 {
			continue // 非回應（通知/解析失敗）→ v1 忽略
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
	// 讀取結束（EOF / 進程退出）：喚醒所有等待者並標記關閉。
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.closeCh)
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp[%s]: 連線已關閉", c.Name)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mcp[%s]: 連線在等待 %s 回應時關閉", c.Name, method)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp[%s] %s 錯誤 %d: %s", c.Name, method, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *Client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n') // stdio 傳輸：換行分隔，訊息內不得含換行
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(b)
	return err
}

// initialize 完成 MCP 握手：initialize 請求 + notifications/initialized 通知。
func (c *Client) initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "cogito-agent", "version": "0.1.0"},
	})
	if err != nil {
		return err
	}
	return c.write(rpcNotification{JSONRPC: "2.0", Method: "notifications/initialized"})
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

// callTool 調用遠端工具，把回傳的 text 內容塊合併成字串；isError=true 時以 error 回傳
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

// Close 關閉連線：結束子進程（若有）。
func (c *Client) Close() error {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return nil
}

// Dial 啟動一個 stdio MCP 伺服器子進程並完成握手。
func Dial(ctx context.Context, cfg ServerConfig) (*Client, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = append(cmd.Environ(), cfg.envSlice()...)

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
	// 子進程 stderr 轉印到日誌，避免緩衝塞滿阻塞。
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
