# 設計研究：同機多 agent 怎麼對齊（2026-08-05）

> **狀態：研究完成；任務板本身未實作（觸發線首測未達標）。** 這份拆解 Hermes Agent v0.20.0
> 的 Kanban，用來**重新檢查我們自己那條待辦的題目對不對**。兩個產出已落地：
> 觸發條件改成可量測（`scripts/subagent_briefing_cost.py`）、以及量測揪出的
> 「orchestrator 把整份原始碼貼進 task_prompt」已在 `orchestrate` 技能修掉。待辦在 [roadmap-next.md](roadmap-next.md)，記憶層現況在
> [memory-stack-audit.md](memory-stack-audit.md)。

## 一句話

**Hermes 對「同機多 agent 怎麼對齊」的答案不是共享記憶，是共享一塊工作板**——而且它刻意
把共享的東西壓到最少：不共享事實、不共享上下文，**只共享任務狀態，且只有一個元件能寫**。

## 起點：A2A 不是這題的答案

v0.20.0 加了 A2A v1.0（Agent2Agent，Linux Foundation 託管），容易被誤讀成「subagent 溝通
變順」。它的文件講得很直白，講的是**反過來**的事：

> "A2A is for crossing **process / machine / framework** boundaries."
> "When you want multiple agents on the **same machine**, prefer **delegation (in-process
> subagents) or the kanban board**."

A2A 是跟**陌生 agent**（另一台 Hermes、LangChain、CrewAI、Google ADK）互通的線路協定，
JSON-RPC 2.0 over HTTP、agent card 掛 `/.well-known/agent-card.json`、per-peer bearer token。
**同機多 agent 的答案是 Kanban**，所以這份研究的對象是它。

---

## Kanban 的機制

一個 SQLite 檔 `~/.hermes/kanban.db`（WAL 模式），**host 上所有 profile 共用**。

### 資料模型

| 表 | 內容 |
|---|---|
| `tasks` | title / body / **assignee**（profile 名）/ status / tenant / **workspace kind** |
| `task_links` | parent → child 依賴 |
| `task_runs` | **一次嘗試一列**，含 outcome / summary / metadata |
| `task_events` | **append-only** 轉換日誌（WebSocket 即時更新就是 tail 這張表） |

狀態機：`todo → ready → running → done`，外加 `blocked` / `archived` 側支。

### 原子認領

cron 驅動的 dispatcher 用 **`BEGIN IMMEDIATE`** 交易認領 `ready` 任務：插一列 `task_runs`，
讓 `tasks.current_run_id` 指過去。WAL 保證讀取迴圈不擋 dispatcher 寫入。

### 執行單元是 OS 行程，不是同進程 swarm

dispatcher 在任務的 workspace 裡 spawn `hermes -p <assignee> chat`，注入 task id / db 路徑 /
workspace 位置等環境變數。文件原話：**"no in-process subagent swarms"**。

assignee 解析不到就**留在 `ready`** 並記一筆 `skipped_nonspawnable` 事件——不靜默 fallback、
不降級硬跑。

### Workspace kind

`scratch`（暫存目錄，完成即刪）/ `dir:<path>`（絕對路徑共享目錄，保留）/ `worktree`
（`.worktrees/` 下的 git worktree，保留）。**相對路徑在 dispatch 時直接拒絕**，理由寫得很好：
「會對到 dispatcher 當下的 CWD」。

### 依賴閘門與失敗處理

- 所有 parent 都 `done` 才把 child 從 `todo` 自動升到 `ready`。
- spawn 失敗 → 計數器++ → 退回 `ready`；連續達 `kanban.failure_limit`（預設 2）自動 `block` 並記原因。
- 另有協定違規預算：連續 3 次 clean-exit 違規才 auto-block。

### 最關鍵的一句設計聲明

> "Hermes Kanban **owns lifecycle truth**. Worker lanes execute work but **never own that
> truth**; everything they do flows back through the kanban kernel via the `kanban_*` tools."

工具集：`kanban_show / list / create / link / complete / block / unblock / heartbeat /
comment / attach`。worker 拿 task-scoped 權限，orchestrator 拿更廣的路由權。

---

## 為什麼這比「共享記憶」聰明

流傳的那張七層記憶圖，layer 07 寫的是「Shared Memory — one truth, every agent aligned」，
配 conflict resolution / consistency / access control（見 [memory-stack-audit.md](memory-stack-audit.md)）。
Hermes 走的是反方向：

| | 共享記憶（那張圖的設定） | Hermes 的共享工作板 |
|---|---|---|
| 共享什麼 | **事實本身** | **只有任務狀態** |
| 誰能寫 | 所有 agent | **只有 kernel** |
| 衝突解決 | 必須真的實作 | **塌縮成「原子認領」** |
| 各 agent 的記憶 | 混在一起 | **各留各的，完全不共享** |

**共享事實會逼你做衝突解決；共享任務狀態 + 單一寫入者則讓衝突問題整個消失。**
這是整份研究最值得記的一句。

---

## 對照 cogito：零件多半有，缺的是「板」

| 面向 | Hermes Kanban | cogito 現況 |
|---|---|---|
| 共享物件 | SQLite 任務板 | ❌ **無**跨 agent 共享物 |
| 狀態真相歸屬 | kernel 獨佔 | 各 session 自己 |
| 認領粒度 | per-task `BEGIN IMMEDIATE` | ⚠️ **整輪一把 flock**（[scheduler.go:99](../internal/cron/scheduler.go#L99)）——設計上只允許 1 個跑者，不是 N 個 worker |
| 執行單元 | 另起 OS 行程 | 同進程 goroutine（agent-as-tool） |
| 工作區隔離 | scratch / dir / worktree | ✅ worktree 已有（[subagent.go:66](../internal/tools/subagent.go#L66)） |
| 任務依賴 | parent→child 自動升 ready | ❌ 無 |
| 失敗退避 | 計數 → 退回 ready → auto-block | ⚠️ 部分：session `ResumeAttempts` 跨重啟續跑（[session_store.go:30](../internal/context/session_store.go#L30)） |
| 持久事件流 | append-only `task_events` | ⚠️ 有 reporter/SSE，但**不是持久事件表** |
| 任務狀態落地 | `tasks` + `task_runs` | ⚠️ 只有 cron 的 `LastRun/LastStatus/LastError`（[store.go:24](../internal/cron/store.go#L24)），**單筆覆寫、無歷史** |
| 背景任務 | — | ✅ TaskManager（[task.go:76](../internal/tools/task.go#L76)），但 session 級、不跨 agent |

**兩個結構性差異**：

1. **我們的 cron 鎖是「排程器互斥」**（整輪持有），它的是「單一任務認領」。前者天生只能一個
   跑者，後者可以 N 個並行 worker。
2. **我們的子 agent 是同進程 goroutine**，它刻意選了行程隔離。

---

## 這推翻了我們 layer-07 的**題目設定**

[roadmap-next.md](roadmap-next.md) 那條寫的是「per-agent 記憶**寫半邊** + 互見」，
觸發條件是「orchestrator 實跑確認每次從零開始真的痛」。

Hermes 的設計等於在說：**那個痛的解法不是讓 agent 互看記憶，是讓任務本身有持久狀態。**
agent 撿起一張卡，卡上已經有 `task_runs` 歷史與 `kanban_comment`——它不需要「記得」上次，
因為**上下文在卡上，不在 agent 腦裡**。

若這個判斷成立，那條待辦要改的是**題目**而不只是排期：

| | 舊題目 | 新題目 |
|---|---|---|
| 要解的 | 具名 agent 跨次任務記憶不互見 | 跨次任務的**工作狀態**沒有落點 |
| 做法 | per-agent 記憶寫半邊 + 治理放行 | 任務板：狀態機 + 持久 run 歷史 |
| 風險 | 淨新增子 agent 反思點 + per-agent 治理面 | 新增一份共享狀態（但單一寫入者） |

---

## 真要做，最小形狀是什麼

**不要照抄 SQLite + process-per-task。** 有價值的是**狀態模型**，不是行程模型：

- **值得抄**：狀態機（`todo/ready/running/done/blocked`）、單一寫入者、原子認領、
  一次嘗試一列的 `task_runs`、append-only 事件、依賴閘門、失敗計數→auto-block、
  相對路徑在 dispatch 時拒絕。
- **不必抄**：SQLite（我們的量級是幾十張卡不是幾萬）、process-per-task（同進程 subagent
  一樣可以認領卡；行程隔離解的是另一個問題）、WebSocket（既有 SSE 夠用）。

形狀上最接近的既有物是 [`internal/cron/store.go`](../internal/cron/store.go)——JSON + 原子寫
（temp + rename）+ flock。把它從「單筆覆寫 LastStatus」擴成「狀態機 + run 歷史」，
再把 [`scheduler.go`](../internal/cron/scheduler.go) 的**整輪鎖換成 per-task 認領**，
就是我們版本的板子。規模估計：中等，不是小改。

> ponytail: 我們的量級不需要資料庫。`flock` + JSONL 事件檔在幾十張卡下毫無壓力；
> 真到需要索引再換 SQLite——狀態模型不變，只換持久層。

---

## 誠實的保留

- **Hermes 自己承認外部 worker 沒鋪好**：「not yet a paved path」——custom `spawn_fn` 的
  auth 與 workspace 對應都還沒定案。所以「任意框架的 worker 都能接板子」目前是願景不是現況。
- **行程隔離比同進程重**。cogito 的賣點是單 binary、近零依賴，`process-per-task` 跟那個
  定位有張力。這也是上面建議「只抄狀態模型」的原因。
- **這份研究來自文件，不是實跑**。沒有實測 Hermes 的板子在多 worker 下的行為。

## 觸發條件（已改成可量測）

原本寫「orchestrator 實跑喊痛」——但沒寫**怎麼知道痛了**。沒有量測計畫的觸發條件，
實質上是無限期延後包裝成紀律。現在有腳本：

```bash
python3 scripts/subagent_briefing_cost.py
```

它掃既有 session，算出「每次委派要把脈絡重講多少字」——那就是「每次從零開始」的直接成本。
兩條線任一達標才重新考慮任務板：**累計 ≥ 50k tokens**、或**單 session 重複派同型 agent ≥ 5 次**。

### 2026-08-05 首測：**還不痛**

| | 實測 |
|---|---|
| 委派次數 | 30 次 / 9 個 session |
| 每次重新交代 | 中位數 **687 字元**、最長 3050、最短 127 |
| 累計 | 26,988 字元 ≈ **13.5k tokens** ≈ **$0.07**（opus 計價） |
| 具名 agent | 24/30 次 |

**結論與直覺相反。** 我原本假設「子 agent 每次從零開始」是明顯的浪費，實測是幾分錢——
任務板要解的問題在目前用量下不值那個工程量。**這正是先量再做的價值：省下一次中等規模的重寫。**

### 但量測揪出另一個問題（已修）

最貴的 4 次（>2000 字元，佔總量一半以上）全是 orchestrator 把**整份原始碼貼進 `task_prompt`**。

那不是「記憶不互見」——子 agent 明明和 orchestrator **共用同一個工作區**，自己讀得到檔案。
是提示詞沒講清楚。已在 `orchestrate` 技能補上「**給路徑，不要貼內容**」，並附上這組數字當理由。

> 這條也是這份研究最實際的產出：**一個看起來需要新架構的問題，實際上是一句提示詞。**

---

## 這份文件的長期用途

下次有人說「我們該做 shared memory」，先問一句——**你要的是共享事實，還是共享工作狀態？**
然後跑一次上面那支腳本，看數字站在哪邊。
