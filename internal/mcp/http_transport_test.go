package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeHTTPServer 是一個最小的 Streamable HTTP MCP 伺服器：initialize 回 JSON + 設 Mcp-Session-Id，
// tools/list 回 JSON，tools/call 回 SSE（驗 client 兩種回應都能處理）。並記錄收到的關鍵頭。
func fakeHTTPServer(t *testing.T) (*httptest.Server, *capturedHeaders) {
	t.Helper()
	cap := &capturedHeaders{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)

		cap.record(r, msg.Method)

		// 通知（無 id）→ 202 無 body
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		id := *msg.ID
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-123")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fake","version":"1"}}}`, id)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","description":"echo it","inputSchema":{"type":"object"}}]}}`, id)
		case "tools/call":
			// 走 SSE：先夾一個無關通知，再給真正的回應（驗 client 會略過前者）
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"hi-from-sse\"}],\"isError\":false}}\n\n", id)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	return srv, cap
}

type capturedHeaders struct {
	auth          string
	acceptHasBoth bool
	sessionOnList string // tools/list 時收到的 Mcp-Session-Id
	protoOnList   string
}

func (c *capturedHeaders) record(r *http.Request, method string) {
	if method == "tools/list" {
		c.auth = r.Header.Get("Authorization")
		ac := r.Header.Get("Accept")
		c.acceptHasBoth = strings.Contains(ac, "application/json") && strings.Contains(ac, "text/event-stream")
		c.sessionOnList = r.Header.Get("Mcp-Session-Id")
		c.protoOnList = r.Header.Get("MCP-Protocol-Version")
	}
}

func TestHTTPTransport_DialListAndCall(t *testing.T) {
	srv, cap := fakeHTTPServer(t)
	defer srv.Close()

	cfg := ServerConfig{Name: "twinkle", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer sk-test"}}
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Dial(HTTP) 失敗: %v", err)
	}
	defer c.Close()

	// tools/list（JSON 路徑）
	tools, err := c.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools 失敗: %v", err)
	}
	if len(tools) != 1 || tools[0].exposedName != "twinkle__echo" {
		t.Fatalf("工具映射錯誤: %+v", tools)
	}

	// tools/call（SSE 路徑，且要略過前面的無關通知）
	out, err := c.callTool(context.Background(), "echo", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("callTool 失敗: %v", err)
	}
	if out != "hi-from-sse" {
		t.Errorf("SSE 回應解析錯誤,got %q", out)
	}

	// 驗證送出的頭：Authorization 轉發、Accept 兩種都列、session/protocol 在 init 後帶上
	if cap.auth != "Bearer sk-test" {
		t.Errorf("Authorization 未轉發,got %q", cap.auth)
	}
	if !cap.acceptHasBoth {
		t.Error("Accept 應同時列 application/json 與 text/event-stream")
	}
	if cap.sessionOnList != "sess-123" {
		t.Errorf("後續請求應帶 initialize 給的 Mcp-Session-Id,got %q", cap.sessionOnList)
	}
	if cap.protoOnList != "2025-06-18" {
		t.Errorf("後續請求應帶協商出的 MCP-Protocol-Version,got %q", cap.protoOnList)
	}
}

// 伺服器把 JSON-RPC id 回成【字串】（"3" 而非 3）——spec 允許,client 必須仍能比對到回應。
func TestHTTPTransport_StringID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		if msg.ID == nil { // 通知
			w.WriteHeader(http.StatusAccepted)
			return
		}
		switch msg.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"%d","result":{"protocolVersion":"2025-06-18"}}`, *msg.ID)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			// 關鍵：id 回成字串 "3"
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"%d\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok-str-id\"}]}}\n\n", *msg.ID)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	c, err := Dial(context.Background(), ServerConfig{Name: "t", URL: srv.URL})
	if err != nil {
		t.Fatalf("Dial 失敗（initialize 字串 id 也要能解）: %v", err)
	}
	defer c.Close()
	out, err := c.callTool(context.Background(), "echo", nil)
	if err != nil || out != "ok-str-id" {
		t.Fatalf("字串型 id 的 SSE 回應應能比對,got out=%q err=%v", out, err)
	}
}

// 收到 404 後必須清掉過期 session,否則後續永遠帶舊 id → 永久 404。
func TestHTTPTransport_404ClearsSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		if msg.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "s1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18"}}`, *msg.ID)
			return
		}
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusNotFound) // 模擬 session 過期
	}))
	defer srv.Close()

	c, err := Dial(context.Background(), ServerConfig{Name: "t", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ht := c.t.(*httpTransport)
	if ht.sessionID != "s1" {
		t.Fatalf("initialize 後應有 session,got %q", ht.sessionID)
	}
	if _, err := c.listTools(context.Background()); err == nil {
		t.Fatal("404 應回 error")
	}
	if ht.sessionID != "" {
		t.Errorf("404 後 sessionID 應被清空,got %q", ht.sessionID)
	}
}

func TestHTTPTransport_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "bad token")
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), ServerConfig{Name: "t", URL: srv.URL})
	if err == nil {
		t.Fatal("HTTP 401 應讓 Dial 失敗")
	}
}
