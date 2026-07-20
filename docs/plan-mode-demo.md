# Plan Mode 端到端 Demo（per-channel 切換 + 目標錨 + 確定性步驟跳過 + 斷點續跑）

一個自足、可驗證、純本地（無網路）的測試案例，展示 Plan Mode 全套：計畫外部化到 `PLAN.md`/`TODO.md`、
框架每輪注入「原始目標錨」與「權威進度帳本」、以及中斷後從斷點續跑、跳過已完成步驟。

## A. 準備（選配，為了跨重啟續跑最有感）
`.env` 加兩行後重啟 `go run ./cmd/claw`：
```
COGITO_SESSION_DIR=./workspace/.sessions   # 讓 per-channel plan 狀態跨重啟記得
COGITO_AUTO_RESUME=1                        # 暫時性中斷（網路等）後退避自動續跑
```

## B. 在頻道開 Plan Mode（Slack / Telegram 皆可）
```
plan on
```
→ bot 回「🗺️ 已為本頻道開啟 Plan Mode…」。（`plan off` 關、`plan status` 查。per-channel、預設關。）

## C. 交這個多步任務
```
用 Go 幫我做一個小工具 wordfreq：讀取工作區的 input.txt，統計每個英文單字
（不分大小寫）出現次數，依次數由高到低輸出；支援 -n 旗標只印前 N 名。
請附單元測試並確保 go test 通過，最後寫一個簡短 README.md 說明用法。
```
自然拆成 ~6 步：`go mod init` → 建 input.txt → 寫 wordfreq.go → 寫 main(`-n`) → 寫測試 → `go test` → README。

## D. 該看到什麼
1. 頻道工作目錄冒出 **PLAN.md**（目標/選型）與 **TODO.md**（`- [ ] 步驟`）。
   位置：`<專案>/workspace/channels/slack_<頻道ID>/`（Telegram 是 `telegram_<chatID>`）。
2. **TODO.md 的 `- [ ]` 逐步變 `- [x]`**——每做完一步就打勾。
3. bot 進度回推 🛠️/✅，最後交付 + `go test` 通過。

## E. 測「斷點續跑 + 確定性步驟跳過」（最有感）
在 TODO 打了 2~3 個勾時：
1. **Ctrl-C** 停掉 `go run ./cmd/claw`（模擬崩潰／重啟）。
2. 重新 `go run ./cmd/claw`。
3. 頻道打：`繼續`

**預期**：agent **不重做**已打勾的步驟，直接從第一個 `- [ ]` 續。原理：
- `PLAN.md`/`TODO.md` 在 workspace 硬碟上（不隨行程消失）；
- 框架每輪把 **TODO.md 的下一步**當權威進度帳本、**PLAN.md 的目標**當目標錨，注入 system 訊息
  （[internal/context/plan.go](../internal/context/plan.go)、[internal/engine/loop.go](../internal/engine/loop.go)）——續跑點由框架確定性指定，而非模型重讀猜測。

> ⚠️ 若沒設 `COGITO_SESSION_DIR`：重啟後 planMode 歸零，續跑前**先再打一次 `plan on`**（檔案還在，一樣從斷點續）。

## F. （選）驗目標錨抗漂移
任務進行中插一句無關的話（如「順便幫我查天氣」），看它會不會被目標錨拉回本任務、而非跑去查天氣。

## 成本
opus、~6 步、數個回合，約 $0.2–0.5。想更省可縮成「wordfreq 不含 README、不含 `-n` 旗標」。

## 對照：這條 demo 涉及的機制
| 觀察點 | 機制 | 檔案 |
|---|---|---|
| PLAN/TODO 生成、續跑紀律 | Plan Mode 系統提示 | [composer.go](../internal/context/composer.go) |
| per-channel 開關、狀態持久化 | `plan on/off`、Session.planMode | [chatbot/core.go](../internal/chatbot/core.go)、[session.go](../internal/context/session.go) |
| 每輪注入「下一步」「原始目標」 | 確定性帳本解析 + 錨注入 | [plan.go](../internal/context/plan.go)、[loop.go](../internal/engine/loop.go) |
| 暫時性中斷自動續跑 | `COGITO_AUTO_RESUME` | [chatbot/core.go](../internal/chatbot/core.go) |
