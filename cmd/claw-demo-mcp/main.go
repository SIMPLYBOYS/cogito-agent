// cmd/claw-demo-mcp 是 MCP 接入的驗收 demo：連接 .mcp.json 裡的外部 MCP 伺服器，列出其工具，
// 並可用 -call 直接調用一個工具（經 Registry，與引擎執行同一路徑）——純 MCP，不經 LLM/Slack。
//
//	export COGITO_MCP_CONFIG=./.mcp.json
//	go run ./cmd/claw-demo-mcp                                   # 連接並列出所有工具
//	go run ./cmd/claw-demo-mcp -call everything__echo -args '{"message":"hi"}'   # 直接調用驗證 tools/call
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/SIMPLYBOYS/cogito-agent/internal/mcp"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	cfgPath := flag.String("config", os.Getenv("COGITO_MCP_CONFIG"), ".mcp.json 路徑")
	callName := flag.String("call", "", "要調用的工具（exposed 名，如 filesystem__list_directory）")
	callArgs := flag.String("args", "{}", "調用參數（JSON）")
	flag.Parse()

	if *cfgPath == "" {
		log.Fatal("請用 -config 或環境變數 COGITO_MCP_CONFIG 指定 .mcp.json（可 cp .mcp.json.example .mcp.json）")
	}

	servers, err := mcp.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("讀取 MCP 設定失敗: %v", err)
	}
	if len(servers) == 0 {
		log.Fatal("設定中沒有任何 mcpServers")
	}

	registry := tools.NewRegistry()
	var clients []*mcp.Client
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()

	ctx := context.Background()
	for _, s := range servers {
		log.Printf(">>> 連接 MCP server %q: %s %v", s.Name, s.Command, s.Args)
		cl, errDial := mcp.Dial(ctx, s)
		if errDial != nil {
			log.Printf("    ❌ 連接失敗: %v", errDial)
			continue
		}
		clients = append(clients, cl)
		ts, errList := cl.Tools(ctx)
		if errList != nil {
			log.Printf("    ❌ tools/list 失敗: %v", errList)
			continue
		}
		for _, t := range ts {
			registry.Register(t)
		}
		log.Printf("    ✅ 發現 %d 個工具", len(ts))
	}

	log.Println("\n===== 已註冊的 MCP 工具 =====")
	defs := registry.GetAvailableTools()
	if len(defs) == 0 {
		log.Fatal("沒有任何工具被註冊（檢查 server 是否啟動成功）")
	}
	for _, def := range defs {
		log.Printf("  • %s — %s", def.Name, def.Description)
	}

	if *callName == "" {
		log.Println("\n（加 -call <工具名> -args '<JSON>' 可直接調用一個工具，驗證完整的 tools/call 路徑）")
		return
	}

	log.Printf("\n===== 調用 %s args=%s =====", *callName, *callArgs)
	res := registry.Execute(ctx, schema.ToolCall{
		ID:        "demo-1",
		Name:      *callName,
		Arguments: []byte(*callArgs),
	})
	if res.IsError {
		log.Printf("❌ 工具報錯:\n%s", res.Output)
	} else {
		log.Printf("✅ 結果:\n%s", res.Output)
	}
}
