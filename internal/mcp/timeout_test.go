package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCallTimeout_Config(t *testing.T) {
	if got := callTimeout(); got != defaultCallTimeout {
		t.Errorf("未設環境變數應為預設 %v，got %v", defaultCallTimeout, got)
	}
	t.Setenv("COGITO_MCP_TIMEOUT", "45")
	if got := callTimeout(); got != 45*time.Second {
		t.Errorf("應讀秒數，got %v", got)
	}
	t.Setenv("COGITO_MCP_TIMEOUT", "0")
	if got := callTimeout(); got != 0 {
		t.Errorf("0 應表示不限，got %v", got)
	}
	t.Setenv("COGITO_MCP_TIMEOUT", "garbage")
	if got := callTimeout(); got != defaultCallTimeout {
		t.Errorf("無法解析應回退預設，got %v", got)
	}
}

// hangingTransport 模擬「接受連線但永不回應」的 MCP server——沒有 timeout 的話這裡會【永久】卡住，
// 同時永久佔住 engine 的併發信號量令牌。
type hangingTransport struct{}

func (hangingTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	<-ctx.Done() // 只有 ctx 到期才會回來
	return nil, ctx.Err()
}
func (hangingTransport) notify(ctx context.Context, method string, params any) error { return nil }
func (hangingTransport) close() error                                                { return nil }

func TestMCPTool_TimesOutOnHangingServer(t *testing.T) {
	t.Setenv("COGITO_MCP_TIMEOUT", "1") // 1 秒，免得測試跑 5 分鐘

	tool := &mcpTool{
		client:      &Client{Name: "hang", t: hangingTransport{}},
		remoteName:  "slow_query",
		exposedName: "hang__slow_query",
	}

	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), []byte(`{}`))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("應因超時而失敗")
		}
		// 錯誤訊息要能指導模型下一步（別盲目重試），不能只是 context deadline exceeded
		if !strings.Contains(err.Error(), "重試同一個呼叫") {
			t.Errorf("超時錯誤應告訴模型重試無用，got: %v", err)
		}
		if !strings.Contains(err.Error(), "hang__slow_query") {
			t.Errorf("超時錯誤應指出是哪個工具，got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("呼叫沒有在 timeout 後返回——吊死的 server 會永久佔住併發令牌")
	}
}
