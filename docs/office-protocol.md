# 像素辦公室協定（office protocol）v1

> 終端應用與 cogito-agent 的整合面是**這份線上協定**，不是 Go SDK——語言無關，Unity／Python／
> Web UI／VS Code extension 都照這份接。Go 側的 package 全在 `internal/`，刻意不對外開放（見文末）。

契約的可執行正本是 `internal/engine/office_reporter_test.go` 的 `TestOfficeReporterContract`；
本文與它不一致時**以測試為準**，並請回報。

---

## 三個端點

| 方向 | 端點 | 誰呼叫 | 認證 |
|---|---|---|---|
| agent → 橋 | `POST {COGITO_OFFICE_URL}/office/event` | `OfficeReporter`（執行事件投影） | 無 |
| agent → 橋 | `POST {COGITO_OFFICE_URL}/office/chat` | bot 的出訊（完成／失敗／審批卡） | 無 |
| 橋 → agent | `POST {COGITO_HTTP_ADDR}/task` | 橋的 Web 外殼（派工） | `Authorization: Bearer {COGITO_HTTP_TOKEN}` |

前兩個沒有認證，因為它們只往**你自己指定的** `COGITO_OFFICE_URL` 送；第三個能執行任意任務，
故有 token ＋ 預設只准 loopback（見 [.env.example](../.env.example) 的 office 區塊）。

---

## `POST /office/event` — 執行事件投影

```json
{ "v": 1, "agent": "office:p17", "kind": "tool", "label": "bash", "detail": "{\"command\":\"ls\"}" }
```

| 欄位 | 說明 |
|---|---|
| `v` | 協定版本（目前恆為 `1`）。**只有不相容變更才進版號**——加欄位不算，橋端請忽略未知欄位 |
| `agent` | **命名空間化的會話身分**，如 `office:p17`／`slack:C123`／`telegram:-100123:456`。同一會話恆定，橋端據此把事件黏到同一個 NPC |
| `kind` | 見下表 |
| `label` | 依 `kind` 而異（**注意 `msg` 的內容在這裡，不在 `detail`**） |
| `detail` | 依 `kind` 而異；空字串時**整個欄位省略**（`omitempty`） |

### `kind` 全集與欄位語意

| `kind` | `label` | `detail` | 何時 |
|---|---|---|---|
| `start` | 任務文字（≤80 字元） | **工作目錄絕對路徑** | 任務開始 |
| `turn` | 回合序號（`"1"`、`"2"`…） | — | 每進入一個 ReAct 回合 |
| `think` | — | — | 進入思考階段 |
| `tool` | 工具名 | 參數 JSON（≤120 字元） | 工具呼叫前 |
| `result` | 工具名 | 輸出（≤400 字元） | 工具成功 |
| `error` | 工具名 | 錯誤輸出（≤400 字元） | 工具失敗 |
| `msg` | **訊息全文**（≤2000 字元） | — | agent 產出文字（供「點 NPC 看報告」面板；泡泡顯示請橋端自行截短） |
| `done` | `"ok"` 或 `"error"` | 錯誤訊息（≤120 字元，僅 `error` 時） | 任務結束 |

超長內容以 `…`（U+2026）結尾。長度單位是**字元（rune）不是位元組**。

### 子 agent 事件的兩種前綴

多 agent 編排時，`tool`／`result`／`error` 的 `label` 會帶前綴，橋端可據此把事件歸到子 agent：

| 形態 | 來源 | 例 |
|---|---|---|
| `[Subagent:<agent_type>] <工具名>` | 子 agent **內部**的工具呼叫 | `[Subagent:correctness] read_file` |
| `spawn_subagent:<agent_type>` | 主 agent **委派**這個動作本身 | `spawn_subagent:performance` |

未指定 `agent_type` 的預設探路者為 `[Subagent] <工具名>`（無冒號）。

---

## `POST /office/chat` — agent 的出訊

```json
{ "agent": "office:p17", "text": "✅ 任務完成（本次花費 $0.0123）" }
```

任務完成／失敗訊息、審批卡都走這裡，顯示在 Web 工作串。`agent` 同上（命名空間化）。
**此端點目前沒有 `v` 欄位**——它只有一個穩定欄位組，未來若需演進會跟進。

---

## `POST /task` — 派工進 agent

```
Authorization: Bearer {COGITO_HTTP_TOKEN}
Content-Type: application/json
```
```json
{ "agent": "p17", "text": "看一下 CI 為什麼紅" }
```

| 回應 | 意義 |
|---|---|
| `202` `{"ok":true}` | 已受理（**任務在背景跑**，結果之後經 `/office/chat` 與 `/office/event` 回來） |
| `400` | 缺 `agent` 或 `text`、JSON 壞掉，或 body 超過 1 MB |
| `401` | token 不符 |
| `405` | 非 POST |

注意：
- **`agent` 這裡是裸 persona id**（`p17`），不帶 `office:` 前綴——agent 端會自己加上命名空間。
- **派工者身分由伺服器端決定**（`COGITO_HTTP_USER`，預設 `office-web`），不從 body 取；
  該身分必須列在 `COGITO_ALLOWED_USERS`，否則 fail-closed 拒絕。
- 指令（`stop`／`status`／`approve`／`get <路徑>`…）也走這個端點，當一般 `text` 送即可。

---

## 傳遞保證（重要：這是**投影**，不是訊息佇列）

| 性質 | 保證 |
|---|---|
| 送達 | **不保證**。fire-and-forget、無重試、無 ack |
| 掉幀 | **會**。緩衝 64 筆，滿了直接丟；收工後到達的事件也丟 |
| 順序 | 同一 `agent` 的事件**保序**（單一 sender 依序送）；跨 agent 無保證 |
| 逾時 | 每個請求 2 秒；任務收尾排空總預算 2 秒（半死的橋不會拖住 agent） |
| 回應 | **agent 完全忽略你的狀態碼與 body**——橋端回什麼都不影響 agent |

設計意圖：辦公室是**狀態投影**，掉幀無害，但**絕不能反壓 agent 主迴圈**。
所以橋端慢或掛掉都是安全的；別把它當成可靠事件流來做帳。

橋不在線（連線被拒）時 agent 靜默丟棄、不記錯誤、不重試——這是刻意的，避免 log 被刷滿。

---

## 版本演進規則

- **加欄位** → 不進版號。橋端**必須忽略未知欄位**（Pydantic 預設即如此；別設 `extra="forbid"`）。
- **改欄位語意／刪欄位／改 `kind` 意義** → `v` 進位，並在本文開一節說明差異。
- 橋端建議：`v` 不認得時仍盡量渲染（`kind` 保守處理），而不是整包丟棄。

---

## 為什麼不是 Go SDK

Go 側 19 個 package **全在 `internal/`**，對外不可 import。這是刻意的：

- **沒有外部 Go 消費者**——橋是 Python/Unity，Go SDK 幫不上；HTTP 契約才是它要的。
- **內部接縫還在動**。近例：`RegisterCoreTools`、`NewPromptComposer`、`NewConsolidateTool`
  各加了一個參數，三個 hook setter 併成 `Hooks`——這些在公開 SDK 下都是 breaking change。
  現在能自由重構，正是因為沒有對外承諾。
- 真要開放的那天，成本是「de-internal ＋ 承諾 API 穩定 ＋ 版本化」，那是**不可逆**的決定。

需要別種整合面（gRPC／WebSocket／SSE）時，加在這一層即可——**agent 內部結構不必動**。
