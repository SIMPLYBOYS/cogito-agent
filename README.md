# cogito-agent

> 一個用 Go 實現的極簡 AI 編程 Agent —— 接入 Slack，由 Claude 驅動，能夠自主"思考 → 調用工具 → 觀察結果"循環，在指定工作區內讀寫文件、執行命令來完成編程任務。

`cogito-agent` 是一個輕量級的自主智能體（Agent）框架。它把一個由 Anthropic Claude 驅動的 Agent 引擎接入 Slack：你在 Slack 中 @機器人或私聊它，它就會在鎖定的工作目錄內自主執行任務，並把思考過程、工具調用和結果實時回推到會話中。

## Features

**核心引擎**
- 🤖 **自主 Agent 循環**：Thinking（慢思考）→ Action（調用工具）→ Observation（觀察結果）的多輪 ReAct 循環，直到任務完成。
- 🧠 **Claude 驅動 / 可替換 Provider**：基於 Anthropic 官方 Go SDK（`anthropic-sdk-go`），透過統一的 `LLMProvider` 接口（`Generate` + `MaxContextTokens`）對接，便於替換底層模型。

**內置工具**（全部在鎖定的工作區內運行）
- `read_file`（超長自動截斷至 8000 字節）、`write_file`（自動創建目錄）、`edit_file`（局部字符串替換，L4 模糊匹配會**自動對齊縮排**）、`bash`（任意命令，帶 30s 超時保護，合併 stdout/stderr）。
- 🧭 **`spawn_subagent`**：把深度探索委派給受限的只讀子智能體（agent-as-tool），上下文隔離、可**並行派出多路偵察兵**。
- 🔌 **可插拔註冊表 + 環繞式中間件**：實現 `BaseTool` 即可註冊；`Registry.Use` 掛載環繞式中間件（審批 / 計時等）。

**駕馭工程（失控控制）**
- 🛡️ **HITL 危險指令審批**：命中黑名單（`rm -rf` / `sudo` / 覆蓋 `.go` / `kill` …）的調用會被掛起，把審批請求推回 Slack 頻道，回 `approve` / `reject` 才放行（含超時自動拒絕）。
- 🚦 **三道硬防線**：主循環**回合上限**（默認 40）、**死循環指紋探測**（參數正規化看穿尾空格/冗餘路徑微差 + 同工具雙閾值）、**per-task 成本熔斷**（默認 \$1）。
- ⚡ **工具併發限流**：單輪多工具併發執行，由緩衝 channel 信號量限制同時在跑數（默認 5），避免打爆下游。
- 🩹 **錯誤自愈**：工具報錯時由 `RecoveryManager` 注入「下一步怎麼做」的救援指南。

**上下文工程**
- 🗜️ **自適應上下文壓縮**：壓縮水位按模型**真實上下文窗口**（token）設定，並用每次 API 回傳的真實 `PromptTokens` 自校準，自動適配不同窗口的模型。
- 🪟 **滑動窗口 + System Prompt 組裝**：`PromptComposer` 組裝身份/紀律/`AGENTS.md`/技能；支持 **Plan Mode**（狀態外部化到 `PLAN.md` / `TODO.md`，可斷點續傳）與 **Skills**（`.claw/skills`）。

**接入與可觀測性**
- 💬 **Slack 集成**：Events API（Webhook），支持頻道 @提及 與私聊（DM），自動校驗簽名、處理 URL 驗證挑戰、過濾自身消息；**每頻道工作區隔離 + per-WorkDir 鎖**（同目錄序列化、不同頻道並行）。
- 📡 **實時進度回推**：`Reporter` 接口把思考 / 工具調用 / 成敗 / 最終回答（含子智能體進度）實時推到 Slack。
- 💰 **成本追蹤**：`CostTracker` 裝飾器按會話累計 token 與 USD 費用。
- 🔭 **OpenTelemetry 鏈路追蹤**：span 經 OTel SDK 匯出（OTLP → Jaeger / Langfuse / Collector），LLM span 帶 `gen_ai.*` 語意約定；未配置端點時為零成本 no-op。
- 🧩 **MCP 集成**：透過 `COGITO_MCP_CONFIG` 載入 `.mcp.json`，連接外部 [Model Context Protocol](https://modelcontextprotocol.io) 工具伺服器（stdio），其工具以 `<server>__` 前綴註冊，與內建工具並列供模型調用（v1 支援 tools）。

## Architecture

```mermaid
flowchart TB
  HUMAN["人類開發者與運維"]
  IM["Slack webhook 與 CLI"]

  subgraph ENGINE["cogito-agent 引擎"]
    LLM["LLM Provider<br/>Claude Anthropic SDK"]
    COST["CostTracker<br/>USD 成本記帳"]
    LOOP["Main Loop ReAct<br/>回合熔斷 成本熔斷 併發限流"]

    subgraph CTX["上下文工程"]
      COMPOSER["PromptComposer<br/>Plan Mode 與技能組裝"]
      COMPACT["自適應 Compactor<br/>真實窗口自校準"]
      REMIND["ReminderInjector<br/>死循環指紋探測"]
      RECOVER["RecoveryManager<br/>錯誤自愈"]
    end

    subgraph TZ["工具與安全"]
      REG["Tool Registry<br/>環繞式中間件鏈"]
      MW["HITL 審批與計時中間件"]
      PRIM["極簡原語<br/>read write edit bash"]
      SUB["spawn_subagent<br/>並行探路 只讀沙箱"]
    end
  end

  subgraph WS["工作區 per-channel 隔離"]
    ASSETS["共享資產<br/>AGENTS.md 與 skills"]
    PROJ["各頻道目錄<br/>項目代碼與日誌"]
    STATE["狀態外部化<br/>PLAN.md 與 TODO.md"]
  end

  subgraph OBS["可觀測性 OTel"]
    OTEL["OTel SDK OTLP"]
    BACKEND["Jaeger 或 Langfuse"]
  end

  HUMAN -->|指令與審批| IM
  IM -->|事件回推| LOOP
  COMPOSER -->|注入 Context| LOOP
  LOOP -->|Thinking Action| LLM
  LLM --> COST
  COST --> LOOP
  LOOP -->|ToolCall| REG
  REG -->|高危攔截審批| MW
  MW -->|放行| PRIM
  MW -->|放行| SUB
  SUB -.-> PRIM
  ASSETS -->|啟動載入| COMPOSER
  PRIM -->|物理 IO| PROJ
  PRIM --> STATE
  HUMAN -->|隨時干預閱讀| STATE
  LOOP -.->|span| OTEL
  OTEL --> BACKEND
```

目錄結構：

```
cmd/
├── claw/                 Slack 服務端入口（生產用）：裝配 Provider/Registry/Engine/SlackBot + OTel，啟動 HTTP
├── claw-cli/             通用命令行入口（-prompt / -dir / -session / -plan）
├── bench/                自動化評測 runner
└── claw-demo-*/          各能力的自包含演示（session / oom / subagent / observability / trace）
internal/
├── engine/                  Agent 核心引擎
│   ├── loop.go              主循環 + RunSub（子智能體）；回合/成本熔斷、併發限流、死循環探測接線
│   ├── reminder.go          死循環探測（指紋參數正規化 + 同工具雙閾值）
│   ├── reporter.go          進度上報接口 Reporter
│   ├── terminal_reporter.go 終端 Reporter
│   └── context.go           把 session 注入 ctx（供中間件取觸發頻道）
├── context/                 上下文工程
│   ├── composer.go          System Prompt 組裝（身份/紀律/Plan Mode/AGENTS.md/Skills）
│   ├── skill.go             .claw/skills 技能載入
│   ├── compactor.go         自適應上下文壓縮（按真實窗口 + PromptTokens 自校準）
│   ├── recovery.go          工具錯誤自愈（救援指南注入）
│   └── session.go           會話歷史 + 滑動窗口 + 成本記帳
├── provider/                大模型 Provider 抽象
│   ├── interface.go         LLMProvider（Generate + MaxContextTokens）
│   └── claude.go            Anthropic Claude 實現
├── tools/                   工具集、註冊表與中間件
│   ├── registry.go          註冊 / 發現 / 執行 + 環繞式中間件鏈
│   ├── middleware.go        計時中間件（量測工具物理執行耗時）
│   ├── read_file/write_file/edit_file/bash.go   內置工具
│   └── subagent.go          spawn_subagent（agent-as-tool）
├── mcp/                     MCP 客戶端（stdio/JSON-RPC）：連外部工具伺服器，適配成 BaseTool
├── slackbot/                Slack 接入層
│   ├── bot.go               Events API 回調、per-channel 工作區隔離與鎖、SlackReporter
│   └── approval.go          危險指令 HITL 審批
├── observability/           可觀測性
│   ├── trace.go / tracing.go  OTel 鏈路追蹤（OTLP → Jaeger/Langfuse）
│   └── tracker.go           CostTracker（USD 成本記帳裝飾器）
├── eval/                    評測框架（benchmark）
└── schema/                 消息與工具的通用數據結構
```

## Install

從源碼構建：

```bash
git clone https://github.com/SIMPLYBOYS/cogito-agent.git
cd cogito-agent
go build ./...
```

需要 **Go 1.25 或更高版本**。

## Configuration

複製環境變量模板並填入真實值（`.env` 已被 `.gitignore` 忽略，不會被提交）：

```bash
cp .env.example .env
```

需要配置的變量：

| 變量 | 說明 |
|------|------|
| `ANTHROPIC_API_KEY` | Anthropic 官方 API 金鑰，從 <https://console.anthropic.com> 獲取 |
| `SLACK_BOT_TOKEN` | Slack Bot Token（`xoxb-` 開頭），所需 Scopes：`chat:write`、`app_mentions:read`、`im:history` |
| `SLACK_SIGNING_SECRET` | Slack Signing Secret，用於校驗回調請求籤名 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | （選填）OTLP 鏈路追蹤上報端點，指向 Jaeger / Langfuse / OTel Collector；未設則追蹤為 no-op |
| `OTEL_EXPORTER_OTLP_HEADERS` | （選填）OTLP 認證標頭，如 Langfuse 的 `Authorization=Basic <base64(pk:sk)>` |
| `OTEL_TRACES_EXPORTER` | （選填）設為 `console` 時把 span 印到終端（本地除錯，不需後端） |
| `COGITO_MCP_CONFIG` | （選填）`.mcp.json` 路徑；載入並連接外部 MCP 工具伺服器 |

### MCP 工具伺服器（選填）

設定 `COGITO_MCP_CONFIG` 指向一份 `.mcp.json`（格式與 Claude Desktop 同構），啟動時會連接其中的 stdio MCP 伺服器，把它們的工具以 `<server>__<tool>` 之名註冊進來：

```jsonc
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/some/dir"]
    }
  }
}
```

```bash
export COGITO_MCP_CONFIG=./.mcp.json
go run ./cmd/claw   # 啟動日誌會顯示「[mcp] 已掛載 server "filesystem" 的 N 個工具」
```

## Usage

1. 配置好 `.env` 後，啟動服務：

   ```bash
   go run ./cmd/claw
   ```

   服務默認監聽 **48080** 端口，Slack Events 回調入口為 `/webhook/event`。

2. 在 Slack App 後臺的 **Event Subscriptions** 中，將 Request URL 配置為你的公網地址，例如：

   ```
   https://<your-domain>/webhook/event
   ```

   （本地開發可藉助 ngrok 等內網穿透工具暴露 48080 端口。）

3. 在 Slack 中與機器人交互：
   - 在頻道中 **@機器人** 並描述任務；
   - 或直接給機器人發 **私聊（DM）** 消息。

機器人在工作區根目錄 `./workspace/` 下、**每個頻道各自隔離的子目錄** `channels/<頻道ID>/` 內完成任務（同頻道任務序列化、不同頻道並行）；技能與 `AGENTS.md` 則從根 `workspace/` 共享讀取。進度實時回覆到對應會話。

> ⚠️ **安全提示**：`bash` 工具會在服務所在機器上執行任意命令，`write_file` / `edit_file` 會修改文件。請僅在隔離/受控環境中運行，並妥善限制可訪問的工作區。

## Development

```bash
go test ./...      # 運行測試
go vet ./...       # 靜態檢查
go build ./...     # 構建
```

## Contributing

歡迎提交 Issue 與 Pull Request。提交前請先運行 `go test ./...` 和 `go vet ./...`。

## License

基於 [MIT License](LICENSE) 發佈。
