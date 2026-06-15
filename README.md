# cogito-agent

> 一個用 Go 實現的極簡 AI 編程 Agent —— 接入 Slack，由 Claude 驅動，能夠自主"思考 → 調用工具 → 觀察結果"循環，在指定工作區內讀寫文件、執行命令來完成編程任務。

`cogito-agent` 是一個輕量級的自主智能體（Agent）框架。它把一個由 Anthropic Claude 驅動的 Agent 引擎接入 Slack：你在 Slack 中 @機器人或私聊它，它就會在鎖定的工作目錄內自主執行任務，並把思考過程、工具調用和結果實時回推到會話中。

## Features

- 🤖 **自主 Agent 循環**：內置 Thinking（慢思考）→ Action（調用工具）→ Observation（觀察結果）的多輪循環，直到任務完成。
- 🧠 **Claude 驅動**：基於 Anthropic 官方 Go SDK（`anthropic-sdk-go`），通過統一的 `LLMProvider` 接口對接大模型，便於替換底層 Provider。
- 🛠️ **內置文件與命令工具**，全部在鎖定的工作區內運行：
  - `read_file` —— 讀取文件內容（超長自動截斷至 8000 字節）
  - `write_file` —— 創建或覆蓋寫入文件（自動創建目錄）
  - `edit_file` —— 對文件做局部字符串替換
  - `bash` —— 執行任意 bash 命令（帶 30s 超時保護，合併 stdout/stderr）
- 🔌 **可插拔工具註冊表**：通過 `Registry` 接口統一註冊、發現與執行工具，新增工具只需實現 `BaseTool` 接口。
- 💬 **Slack 集成**：基於 Slack Events API（Webhook），支持頻道 @提及 與私聊（DM），自動校驗簽名、處理 URL 驗證挑戰、過濾自身消息避免迴環。
- 📡 **實時進度回推**：通過 `Reporter` 接口將"正在思考 / 正在調用工具 / 執行成功或報錯 / 最終回答"實時推送到 Slack。
- ⚡ **併發工具執行**：單輪內的多個工具調用併發執行，再按序彙總結果。

## Architecture

```
cmd/claw/main.go          程序入口：加載配置、裝配 Provider/Registry/Engine/SlackBot，啟動 HTTP 服務
internal/
├── engine/               Agent 核心引擎
│   ├── loop.go           Thinking → Action → Observation 主循環
│   └── reporter.go       進度上報接口 Reporter
├── provider/             大模型 Provider 抽象
│   ├── interface.go      統一的 LLMProvider 接口
│   └── claude.go         Anthropic Claude 實現
├── tools/                工具集與註冊表
│   ├── registry.go       工具註冊 / 發現 / 執行
│   ├── read_file.go      read_file 工具
│   ├── write_file.go     write_file 工具
│   ├── edit_file.go      edit_file 工具
│   └── bash.go           bash 工具
├── slackbot/             Slack 接入層
│   └── bot.go            Events API 回調、SlackReporter
└── schema/               消息與工具的通用數據結構
    └── message.go
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

機器人會在鎖定的工作目錄（服務進程的當前目錄）內自主完成任務，並把進度實時回覆到對應會話。

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
