// cmd/claw-demo-mcp 是 MCP 接入的驗收 demo：連接 .mcp.json 裡的外部 MCP 伺服器，列出其工具，
// 並可用 -call 直接呼叫一個工具（經 Registry，與引擎執行同一路徑）——純 MCP，不經 LLM/Slack。
//
//	export COGITO_MCP_CONFIG=./.mcp.json
//	go run ./cmd/claw-demo-mcp                                   # 連接並列出所有工具
//	go run ./cmd/claw-demo-mcp -call everything__echo -args '{"message":"hi"}'   # 直接呼叫驗證 tools/call
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/SIMPLYBOYS/cogito-agent/internal/cmdutil"
	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/mcp"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

func main() {
	// 載入 .env + 初始化 OTel（單一 bootstrap，避免漏接 InitTracing）。
	defer cmdutil.Bootstrap("cogito-agent-demo-mcp")()

	cfgPath := flag.String("config", os.Getenv("COGITO_MCP_CONFIG"), ".mcp.json 路徑")
	callName := flag.String("call", "", "要呼叫的工具（exposed 名，如 filesystem__list_directory）")
	callArgs := flag.String("args", "{}", "呼叫參數（JSON）")
	prompt := flag.String("prompt", "", "給 agent 的任務（設了則由 Claude 經 ReAct 迴圈自行呼叫 MCP 工具，需 ANTHROPIC_API_KEY）")
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
		log.Printf("    ✅ 已連接")
	}
	if len(clients) == 0 {
		log.Fatal("沒有任何 MCP server 連接成功")
	}

	// 經 gateway 漸進式暴露：只註冊 mcp_call_tool / mcp_describe_tool 兩個工具（與 cmd/claw 同路徑）。
	gw, err := mcp.NewGateway(ctx, clients)
	if err != nil {
		log.Fatalf("建立 gateway 失敗: %v", err)
	}
	for _, gt := range gw.Tools() {
		registry.Register(gt)
	}

	log.Printf("\n===== MCP 工具目錄（共 %d 個，經 mcp_call_tool 呼叫）=====", gw.Count())
	for _, name := range gw.Names() {
		log.Printf("  • %s", name)
	}

	// 模式 C（L2 全鏈路）：由 Claude 經 ReAct 迴圈自行 mcp_describe_tool / mcp_call_tool。
	if *prompt != "" {
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			log.Fatal("-prompt 模式需要 ANTHROPIC_API_KEY")
		}
		workDir, _ := os.MkdirTemp("", "claw_mcp_demo")
		llm := provider.NewClaudeProvider("claude-opus-4-8")
		eng := engine.NewAgentEngine(llm, registry, false, false)
		sess := ctxpkg.NewSession("mcp-demo", workDir)
		sess.Append(schema.Message{Role: schema.RoleUser, Content: *prompt})
		log.Printf("\n===== L2：agent 執行任務（自行經 gateway 呼叫 MCP 工具）=====\n>>> %s", *prompt)
		if err := eng.Run(ctx, sess, engine.NewTerminalReporter()); err != nil {
			log.Fatalf("agent 執行失敗: %v", err)
		}
		return
	}

	// 模式 B：直接呼叫一個工具（經 gateway 的 mcp_call_tool，不經 LLM）。
	if *callName != "" {
		log.Printf("\n===== 經 gateway 呼叫 %s args=%s =====", *callName, *callArgs)
		wrapped := fmt.Sprintf(`{"name":%q,"arguments":%s}`, *callName, *callArgs)
		res := registry.Execute(ctx, schema.ToolCall{
			ID:        "demo-1",
			Name:      "mcp_call_tool",
			Arguments: []byte(wrapped),
		})
		if res.IsError {
			log.Printf("❌ 工具報錯:\n%s", res.Output)
		} else {
			log.Printf("✅ 結果:\n%s", res.Output)
		}
		return
	}

	// 模式 A：僅列出目錄（預設）。
	log.Println("\n（-call <工具名> -args '<JSON>' 經 gateway 直接呼叫；-prompt '<任務>' 讓 Claude 自行呼叫驗全鏈路）")
}
