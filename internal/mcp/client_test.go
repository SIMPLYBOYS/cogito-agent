package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
)

// fakeServer 是 in-process 的最小 MCP 伺服器：從 reqR 讀 JSON-RPC 行，往 respW 回應。
// 支援 initialize / tools/list / tools/call（echo 回傳 msg、boom 回 isError）。
func fakeServer(reqR io.Reader, respW io.Writer) {
	sc := bufio.NewScanner(reqR)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	write := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = respW.Write(append(b, '\n'))
	}
	for sc.Scan() {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		if req.ID == 0 { // 通知（如 notifications/initialized）→ 不回應
			continue
		}
		switch req.Method {
		case "initialize":
			write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": protocolVersion, "capabilities": map[string]any{},
				"serverInfo": map[string]any{"name": "fake", "version": "1"},
			}})
		case "tools/list":
			write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"tools": []map[string]any{
					{"name": "echo", "description": "回傳 msg", "inputSchema": map[string]any{
						"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}},
					}},
					{"name": "boom", "description": "永遠報錯", "inputSchema": map[string]any{"type": "object"}},
				},
			}})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name == "boom" {
				write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "kaboom"}}, "isError": true,
				}})
				continue
			}
			msg, _ := p.Arguments["msg"].(string)
			write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": msg}}, "isError": false,
			}})
		}
	}
}

// dialFake 用 io.Pipe 接好 client ↔ fakeServer。
func dialFake(t *testing.T) *Client {
	t.Helper()
	c2sR, c2sW := io.Pipe() // client → server
	s2cR, s2cW := io.Pipe() // server → client
	go fakeServer(c2sR, s2cW)
	c := newClient("test", c2sW, s2cR, nil)
	if err := c.initialize(context.Background()); err != nil {
		t.Fatalf("initialize 失敗: %v", err)
	}
	return c
}

func TestClient_ListAndCall(t *testing.T) {
	c := dialFake(t)

	tools, err := c.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools 失敗: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("應有 2 個工具，got %d", len(tools))
	}
	// 名字應加上 server 前綴
	if tools[0].Name() != "test__echo" {
		t.Errorf("工具名應為 test__echo，got %q", tools[0].Name())
	}

	// echo 正常回傳
	out, err := tools[0].Execute(context.Background(), []byte(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("echo 執行失敗: %v", err)
	}
	if !strings.Contains(out, "hello") { // 結果被包在「不受信外部資料」邊界標記內
		t.Errorf("echo 回傳應含 hello，got %q", out)
	}

	// boom 應以 error 回傳（isError → error-as-observation）
	if _, err := tools[1].Execute(context.Background(), []byte(`{}`)); err == nil {
		t.Error("boom 應回傳 error")
	}
}

// 併發呼叫驗證 id 路由正確（我們的工具是並行執行的）。
func TestClient_ConcurrentCalls(t *testing.T) {
	c := dialFake(t)
	tools, _ := c.Tools(context.Background())
	echo := tools[0]

	var wg sync.WaitGroup
	results := make([]string, 20)
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := string(rune('A' + idx))
			out, err := echo.Execute(context.Background(), []byte(`{"msg":"`+msg+`"}`))
			if err == nil {
				results[idx] = out
			}
		}(i)
	}
	wg.Wait()
	for i, got := range results {
		want := string(rune('A' + i))
		if !strings.Contains(got, want) { // 每個結果含各自 msg＝id 路由正確（結果外層有邊界標記）
			t.Errorf("併發呼叫 %d：id 路由錯誤，want 含 %q got %q", i, want, got)
		}
	}
}

// MCP server 子行程不得繼承本行程的金鑰。
//
// 這些 server 多半是 npx/uvx 拉下來的第三方套件，把 ANTHROPIC_API_KEY / bot token 全交出去
// 等於供應鏈曝險。server 真正需要的憑證應由 .mcp.json 的 env 明確宣告。
func TestDialStdio_DoesNotInheritSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-reach-mcp")
	t.Setenv("SLACK_BOT_TOKEN", "token-should-not-reach-mcp")

	// 讓 server 把自己看到的環境變數印出來後結束；dial 會因握手失敗回錯，但我們要的是它的 stdout。
	cfg := ServerConfig{
		Command: "bash",
		Args:    []string{"-c", "env > " + t.TempDir() + "/dummy; env"},
		Env:     map[string]string{"DECLARED_TOKEN": "explicitly-configured"},
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = append(sandbox.FilteredEnv(), cfg.envSlice()...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("執行失敗: %v", err)
	}
	seen := string(out)

	for _, leaked := range []string{"sk-should-not-reach-mcp", "token-should-not-reach-mcp"} {
		if strings.Contains(seen, leaked) {
			t.Errorf("MCP server 看得到金鑰 %q", leaked)
		}
	}
	// .mcp.json 明確宣告的 env 必須照樣送達，否則 server 自己的憑證會壞掉
	if !strings.Contains(seen, "DECLARED_TOKEN=explicitly-configured") {
		t.Error(".mcp.json 明確宣告的 env 應保留")
	}
	// npx/uvx 靠這兩個找執行檔與快取
	for _, need := range []string{"PATH=", "HOME="} {
		if !strings.Contains(seen, need) {
			t.Errorf("缺少 %s，npx/uvx 會跑不起來", need)
		}
	}
}
