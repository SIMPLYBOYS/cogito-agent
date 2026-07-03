package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

func toolByName(ts []tools.BaseTool, name string) tools.BaseTool {
	for _, t := range ts {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func TestGateway_CatalogAndCall(t *testing.T) {
	c := dialFake(t)
	gw, err := NewGateway(context.Background(), []*Client{c})
	if err != nil {
		t.Fatalf("NewGateway 失敗: %v", err)
	}
	if gw.Count() != 2 {
		t.Fatalf("目錄應有 2 個工具，got %d", gw.Count())
	}

	gwTools := gw.Tools()
	if len(gwTools) != 2 {
		t.Fatalf("gateway 應暴露 2 個工具，got %d", len(gwTools))
	}

	call := toolByName(gwTools, "mcp_call_tool")
	describe := toolByName(gwTools, "mcp_describe_tool")
	if call == nil || describe == nil {
		t.Fatal("應有 mcp_call_tool 與 mcp_describe_tool")
	}

	// 目錄（輕量）應出現在 call 工具的描述裡，但【不含】完整 schema。
	desc := call.Definition().Description
	if !strings.Contains(desc, "test__echo") || !strings.Contains(desc, "test__boom") {
		t.Errorf("call 工具描述應含目錄: %s", desc)
	}

	// 透過 gateway 調用底層工具
	out, err := call.Execute(context.Background(), []byte(`{"name":"test__echo","arguments":{"msg":"via-gateway"}}`))
	if err != nil {
		t.Fatalf("mcp_call_tool 執行失敗: %v", err)
	}
	if !strings.Contains(out, "via-gateway") { // 結果被包在「不受信外部資料」邊界標記內
		t.Errorf("回傳應含 via-gateway，got %q", out)
	}

	// 未知工具名 → error
	if _, err := call.Execute(context.Background(), []byte(`{"name":"nope"}`)); err == nil {
		t.Error("未知工具應回 error")
	}

	// describe 應回完整 schema
	d, err := describe.Execute(context.Background(), []byte(`{"name":"test__echo"}`))
	if err != nil {
		t.Fatalf("mcp_describe_tool 失敗: %v", err)
	}
	if !strings.Contains(d, "input_schema") || !strings.Contains(d, "msg") {
		t.Errorf("describe 應含 input_schema 與參數 msg: %s", d)
	}
}
