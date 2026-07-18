// Package agentkit 收斂「組裝一個 agent」的共用零件，消掉 cmd/claw（bot）、cmd/claw-cli、
// cmd/claw-dashboard 之間逐字重複、且已開始漂移的組裝碼（如子 agent 的工具池、MCP gateway 載入）。
//
// 刻意【不】做成吃十幾個參數的巨型 builder：各入口的站點特有邏輯（bot 的 per-channel model 覆蓋 /
// knobs / summary、dashboard 的 SSE reporter、cli 的 goal-loop）差異太大，硬塞單一簽名反而更複雜。
// 這裡只抽真正共用、穩定、且漂移就發生在其上的三塊：核心工具集、子 agent 佈線、MCP gateway 載入。
package agentkit

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/mcp"
	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// RegisterCoreTools 註冊標準工具集：檔案讀寫/bash/編輯 rooted 在 workDir；技能與長期記憶 rooted 在
// skillMemDir（bot 用共享根 rootDir，cli/dashboard 用 workDir 本身）；外加等寬長條圖。
func RegisterCoreTools(r tools.Registry, workDir, skillMemDir string, executor sandbox.Executor) {
	r.Register(tools.NewReadFileTool(workDir))
	r.Register(tools.NewWriteFileTool(workDir))
	r.Register(tools.NewBashToolWithExecutor(workDir, executor))
	r.Register(tools.NewEditFileTool(workDir))
	r.Register(tools.NewReadSkillTool(skillMemDir))
	r.Register(tools.NewRecallTool(skillMemDir))
	r.Register(tools.NewBarChartTool())
}

// SubagentOpts 是各站點對子 agent 佈線的差異點。
type SubagentOpts struct {
	Executor      sandbox.Executor
	SkillsBaseDir string      // 技能綁定的根（bot=rootDir）
	Reporter      interface{} // 子 agent 進度回報（NewSubagentTool 收 interface{}）
	// Middleware 掛進【子 registry】（bot 的 approval/timing——子 agent 的危險操作也要放行/計時）。
	Middleware []tools.MiddlewareFunc
	// ExtraSubTools 在核心工具外再往【子 registry】加料（如 dashboard 把 MCP gateway 也給子 agent）。
	ExtraSubTools func(r tools.Registry)
}

// WireSubagent 建子 agent 工具（含 worktree 隔離）並註冊進 mainReg，連同背景委派查詢工具
// （subagent_result / subagent_list）。子 registry 是超集工具池（read/bash/write/edit）+ 站點加料。
// 這是先前 bot/cli/dashboard 逐字重複、且漂移（子 MCP、中介層）就發生在其上的區塊。
func WireSubagent(mainReg tools.Registry, runner tools.AgentRunner, baseWorkDir string, opts SubagentOpts) {
	buildSubReg := func(wd string) tools.Registry {
		r := tools.NewRegistry()
		r.Register(tools.NewReadFileTool(wd))
		r.Register(tools.NewBashToolWithExecutor(wd, opts.Executor))
		r.Register(tools.NewWriteFileTool(wd))
		r.Register(tools.NewEditFileTool(wd))
		for _, mw := range opts.Middleware {
			r.Use(mw)
		}
		if opts.ExtraSubTools != nil {
			opts.ExtraSubTools(r)
		}
		return r
	}
	subTool := tools.NewSubagentTool(runner, buildSubReg(baseWorkDir), opts.Reporter, opts.SkillsBaseDir).
		WithWorktreeIsolation(baseWorkDir, buildSubReg)
	mainReg.Register(subTool)
	for _, bt := range subTool.BackgroundTools() {
		mainReg.Register(bt)
	}
}

// LoadMCPGateway 依 COGITO_MCP_CONFIG 連上外部 MCP 伺服器並建 gateway（漸進式暴露）。未設 config 回
// (nil, nil)；連不上的 server 略過、不擋啟動。回傳的 clients 供呼叫端在關閉時 Close（可忽略）。
func LoadMCPGateway(dialTimeout time.Duration) (*mcp.Gateway, []*mcp.Client) {
	cfgPath := os.Getenv("COGITO_MCP_CONFIG")
	if cfgPath == "" {
		return nil, nil
	}
	servers, err := mcp.LoadConfig(cfgPath)
	if err != nil {
		log.Printf("[mcp] 讀取設定失敗，不載 MCP: %v", err)
		return nil, nil
	}
	var clients []*mcp.Client
	for _, s := range servers {
		dialCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		cl, errDial := mcp.Dial(dialCtx, s)
		cancel()
		if errDial != nil {
			log.Printf("[mcp] 連接 %q 失敗，略過: %v", s.Name, errDial)
			continue
		}
		clients = append(clients, cl)
		log.Printf("[mcp] 已連接 server %q", s.Name)
	}
	if len(clients) == 0 {
		return nil, nil
	}
	gwCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	gw, errGw := mcp.NewGateway(gwCtx, clients)
	cancel()
	if errGw != nil {
		log.Printf("[mcp] 建立 gateway 失敗: %v", errGw)
		return nil, clients
	}
	log.Printf("[mcp] gateway 就緒：%d 個外部工具經 mcp_call_tool 漸進式暴露", gw.Count())
	return gw, clients
}

// RegisterMCPTools 把 gateway 的 2 個工具（mcp_call_tool / mcp_describe_tool）註冊進 r。nil-safe。
// 同一組 gateway 工具可註冊進多個 registry（主 + 子），共用底層連線（transport 有 mutex，並發安全）。
func RegisterMCPTools(r tools.Registry, gw *mcp.Gateway) {
	if gw == nil {
		return
	}
	for _, gt := range gw.Tools() {
		r.Register(gt)
	}
}
