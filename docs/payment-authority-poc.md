# 支付授權層 PoC 回顧（FUTUREMODE 2026，9/4–9/6）

> 狀態：**已從主線移除**（2026-09-07），等 spec 完善後重啟。碼與測試完整保留在 git 歷史，
> 本文是重啟時的地圖：做了什麼、什麼是真的、什麼是 mock、量到什麼、踩到什麼、下一版該先決定什麼。

## 一句話

> Agent 是第一種 commit 那一刻沒有人的公司支出。誰批准、憑哪條規則、留不留得下證據。

x402 只標準化「支付協商」（三個 header），custody／預算／agent policy 明說在協議外。這個 PoC 蓋的就是那三件：
agent **只能提請購單**，裁決 Deny > Ask > Allow，Ask 走既有的人在迴路審批，出納簽名、agent 碰不到金鑰，每筆（含被拒）落帳。

## 歷史位置（重啟從這裡拿）

| commit | 內容 |
|---|---|
| `4177757` | `policy.PaymentGate`：六欄位請購單、九條確定性檢查、微美元整數、Decide 不記帳 Commit 才記、漏設定往安全倒 |
| `a9d9f8b` | `request_payment` 工具、x402 v2 wire ＋ 本地 402/facilitator mock、金流稽核帳（Deny 也落）；**同 commit 內的 SoD 修正已保留在主線** |
| `ca1be20` | mock 回標明為範例的小資料集 |
| `1a20251` | **wire 對齊真 v2**：以 test402.com 的真實 402 當 fixture，修掉三個只對自己 mock 成立的欄位錯誤 |
| `5826b40` | mock 註解對齊「nonce 由付款方產」 |
| unity_demo `2d98a65` | 橋：審批卡解成請購單、`/office/audit`；外殼：請購單版面、金流稽核面板（**審批鑰匙分開送的那半已保留**） |
| unity_demo `b87fca4`／`64f8577` | `tools/demo_stack.sh`：一鍵三行程（含 office-only 啟動技巧，見下） |

`git show <sha>` 即可取回；重啟時建議 `git checkout <sha> -- internal/x402 internal/payment internal/policy/payment.go internal/policy/audit.go internal/tools/taskctx.go cmd/x402mock` 再逐一接回。

## 架構（四個切面，x402 只碰到一個）

```
Renderers   辦公室 │ 外殼          ← 只吃事件
Audit       每筆 Decision 一個事件，Deny 也是（append-only JSONL）
Policy      Decide(Intent, runningTask) → Decision   ← 純函式，不知道軌道
Intent      六欄位請購單 ＋ nonce（= mandate）        ← 所有東西的單位
Settler     x402 mock │ 真 facilitator │ 其他軌道     ← 介面，可換
```

刻意的決定與理由：

- **task binding 從 ctx 來，不從 agent 參數來**（`tools.TaskContext`，`handleAgentRun` 起任務時設）。錢包／卡層知道「花多少」不知道「為了哪個 task」，這是 harness 層才做得到、也是這層存在的理由。被帶偏的 agent 若能自填 task id，binding 就是裝飾。
- **簽名權與 agent 分離**（`Signer` 介面在工具內部；OWASP ASI03）。agent 就算被完全接管，能做的只是提一張會被裁決的單。
- **金額全用微美元整數**。USDC 6 位小數 ⇒ atomic units == 微美元，零換算誤差。小數第七位截掉不進位——寧可少算自己的額度。
- **Decide 不記帳、Commit 才記**。裁決 Allow 不等於錢離開；在裁決就記帳會讓失敗的嘗試吃掉預算。
- **漏設定往安全倒**：沒有 `payment` 區塊＝fail-closed 全 Deny；有區塊沒門檻＝一律 Ask；設定解析失敗直接開不起來（0 在比較裡等於把保護關掉）。
- **Deny 走 `ErrPolicyDenied`**（引擎終止該目標，不教它繞）；**人拒絕不標 Denied**（人在現場，理由可引導改路）。
- **結算逾時＝未知**：不 Commit、不重試、明講「別再提一次」。「Timeout 是未知結果，不是再付一次的許可。」
- **三段門檻**（auto_below／ask_below／budget）而非一條上限：例外管理才是內控的真實形狀——人不看每一筆，人只被例外打斷。

## 真的 vs mock（重啟時第一件要對齊的）

| 層 | 狀態 | 備註 |
|---|---|---|
| 裁決矩陣（Allow／Ask／Deny × 預算／商家／過期／replay／task） | 真 | 全單測、逐條拔線驗紅 |
| 人在迴路審批 ＋ 派工權／審批權分離 | 真 | 活跑驗證：不帶審批鑰匙 → `🚫 非管理員 office-web 嘗試 approve，已拒絕`；帶了 → `human:admin` 放行並結算 |
| 稽核帳（含 Deny） | 真 | `workspace/.claw/audit/payments.jsonl` 留有一筆真實的 ASK→核准→結算 |
| x402 v2 wire ＋ client（402 → 解報價 → 綁單 → 簽 → 重送 → 解回應） | 真 | 對 **test402.com 真實端點** 驗過；fixture 存在 `1a20251` 的 `internal/x402/testdata/` |
| 簽名 | **mock** | HMAC-SHA256 共用秘密。真的是 EIP-712 typed data → secp256k1，對 EIP-3009 `transferWithAuthorization` 六欄位 |
| 資源伺服器 ＋ facilitator ＋ 結算 | **mock** | `cmd/x402mock`：VALIDATE→MATCH→RESERVE→SETTLE、nonce 表、`/verify` 唯讀 `/settle` 改狀態 |
| 金鑰階層 | 兩層 | 出納 ＋ 權限分離；沒有可撤銷的 session key（工作坊 §4.6 三層） |
| approver 身分 | 缺 | 帳上記 `admin`，`ApprovalManager` 不回批准者 id |

## 量到的（不是推論）

1. **v2 的欄位跟印象不同。** 第一版 wire 照印象寫成 v1 形狀。拿真報價逐鍵比：每格是 `amount`（`maxAmountRequired` 是 v1）、`resource` 在頂層不在 accept 裡、`extra` 帶 EIP-712 domain（`name`／`version`／`assetTransferMethod`）、**nonce 由付款方產**（EIP-3009），伺服器不給。對著真伺服器第一版會把金額讀成空、nonce 讀成 `<nil>`。教訓：**mock 是自己寫的，自己一定解得開——不算證據**。fixture ＋ `X402_LIVE=1` 的 live 測試才算。
2. **agent 會不會用這顆工具**：會。Haiku 4.5 在「這個研究任務需要 X 的資料，請用 request_payment 取得」一句下直接提單；被人核准後拿到資料、誠實回報 mock 內容沒實質資料可摘要。
3. **一次 demo 任務花費 $0.34（Haiku）**，比預估高——任務後的記憶反思用主模型；`COGITO_REFLECT_MODEL` 可壓。
4. **市場現況**（2026-08，InfoQ）：Cloudflare Wallets 三個控制（allowance／merchant allow-list／max transaction），「spending controls stop at the payment」，跨筆預算與 task 層級政策「left to the application above」；OpenClaw x402 skill 只有 `MAX_SPEND_PER_CALL`。**那個 application above 就是 harness**——這層有人在等。

## 踩到的

- 驗紅腳本用 `git checkout` 還原，把未 commit 的修正一起沖掉；untracked 檔又走到空字串 replace 把區塊黏到檔頭。**還原一律備份 cp。**
- `go run` fork 出真伺服器子行程，kill 只殺得到包裝；上一輪殘留的 cogito 佔著 8787，新的 office 入口 bind 失敗後**行程照樣活著**、畫面看不出異常。**先 `go build -o` 再 exec，啟動前預檢 port。**
- cogito 自己 `godotenv.Load()` 讀 `.env`，unset 會被撿回來。**office-only 啟動要把 `SLACK_BOT_TOKEN`／`SLACK_APP_TOKEN`／`TELEGRAM_BOT_TOKEN` 設成空字串**（godotenv 不覆寫已存在的變數，空也算存在）。
- SoD 的回退邏輯在**兩處**（`NewCore` 與 `authz.Store.Sets()`），只改一邊測試照紅。

## 重啟前要先決定的（spec 待完善）

1. **第一個真用戶是誰。** 最誠實的答案：cogito 自己——它每次任務都在花真錢（LLM token、Tavily）。把請購單套回**自己的計量支出**（任務預算、換貴模型要核准、跨任務累計、誰批的留帳）零 mock、零區塊鏈。x402 是 settler 之一，內部帳是另一個。
2. **結算層要真到哪。** 真 EIP-712 簽名（go-ethereum crypto，約 80–120 行）＋ Base Sepolia testnet USDC；Signer 介面已留縫。或者先不做，內部支出不需要。
3. **scheme 覆蓋**：`exact` 必要；`upto` 對 LLM 用量計費是對的形狀，但「用量由誰證明」是開放問題。
4. **金鑰三層與 session key 綁 task 生命**（task done 即過期）——一條 expiry ≤ task 的檢查就有，但要先定 task 生命的邊界。
5. **approver 身分回傳**：`ApprovalManager.ResolveApproval` 帶 userID 回來，帳上才記人名。
6. **審批 UI 的所有權**：目前兩把鑰匙都在橋的 `.env`，分離是結構上的、部署上仍是名義的。真要落地，審批鑰匙要離開橋。
7. **A2A（NPC 付 NPC）**：辦公室原生的場景（委派→交付→付款），走位已存在；付款方的 intent 綁委派這件事。

## 工作坊對照（York，國泰）

覆蓋：§4.1 五角色、§4.3 六欄位 intent、§4.4 binding、§4.5 policy 五道檢查、§5.2 OWASP ASI01/02/03/08/09、§5.3 fail-closed ＋ Deny 可觀測、§5.4 結算路徑、§6 八步驟。
未覆蓋：真結算（§3）、金鑰三層（§4.6）、ERC-8196／ERC-8004（刻意不碰）、`batch`、§8 KYC／爭議／流動性。

## 相關筆記

- 參戰計畫與 x402 工作坊筆記：aaron-vault `projects/2026-09-futuremode-hackathon/參戰計畫.md`、`research/FUTUREMODE-2026-x402-AI-Agent-支付架構-國泰演講筆記.md`
- 主線保留的兩個修正：Slack opt-in（`beecc8c`）、派工權／審批權分離（`a9d9f8b` 的 office.go／authz／core.go 部分＋ unity_demo 橋端 `cogito_headers`）
