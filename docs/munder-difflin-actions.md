# Action Items：對照 Munder Difflin（2026-08-31）

> 來源：[munderdiffl.in](https://munderdiffl.in/) 與其 repo（`chaitanyagiri/munder-difflin`，
> Electron/JS，2026-05-31 建、三個月 5.7k 星）。讀過官網、README、HIVE.md、
> MEMORY_GRAPH_SPEC.md 與 repo tree；**未讀程式碼與 SECURITY.md**，加密聲明未驗證。
> license 標「Other（非標準）」——**拿想法可以，抄碼前要先確認授權**。
>
> 本筆記是**可逐步執行的清單**，每項附完成條件。判準沿用 roadmap：按「動它的風險」排序，
> 觸發條件擋著的不動。

---

## ✅ 判斷同步（寫筆記當下已完成）

- roadmap「Inter-agent messaging」擱置項已加指標指向本文 §3（觸發條件**不變**，只是觸發時不用重新研究）。
- kg-status §9 已加「自動邊」為候選路線。

---

## 1. 🟢 KG 自動邊：從既有行為推導，不等人寫 `[[links]]`

**是什麼**：他們的記憶圖完全不用 LLM 也不用人工——message edges 從通訊紀錄推導、
topic edges 用「≥2 個 agent 都提過」的啟發式，並自承「This is heuristic, not semantic」。

**對應到我們**（兩種現成資料）：
- **co-recall 邊**：同一 session 被一起 recall 的記錄 → `relates-to`。資料在 memory-usage 帳本。
- **provenance 邊**：從同一任務沉澱的記錄 → `same-origin`。資料在記錄的〔來源 provenance〕行。

**為什麼值得**：正式庫 14 節點 0 邊（見 [kg-status.md](kg-status.md) §4），LLM 提案邊卡兩個月沒人審。
推導邊是第三條路：免費、自動、可重跑、錯了刪掉重推即可——**圖自己長，不等人**。

**依賴**：先修 name 截斷（下一項）。節點 ID 都是斷句時，推出來的邊一樣配不到節點。

**規模**：小～中（一支推導函式 + 寫進 edges.jsonl 走既有 gate；不新增依賴）。

**完成條件**：跑一次推導後 `edges.jsonl` 有 ≥1 條 source 標 `derived` 的邊，
且 `cmd/ingest -recall` 能沿它撈到多跳結果；推導可重跑且冪等（跑兩次不重複）。

### ✅ 已完成（2026-08-31）

`cmd/ingest -derive-edges` → 提案檔 → 既有的 `-review-edges` / `-apply-edges` 流程。
實測 4 個任務推出 **24 條** `same-origin` 邊、過閘 **0 拒絕**，`-recall -hops 2`
撈回 8 個連通節點。**co-recall 邊未做**：帳本（memory-usage.json）只有 `last_used`
與 `hits`，沒有共現紀錄——要做得先擴充帳本，另立工項。

## 2. 🟢 修 KG 節點 name 截斷（既有待辦，優先級因 #1 提高）

**現況**：`memoryTitleRunes = 24` 硬截斷，14 個節點 ID 全是斷句，`[[link]]` 無從指向
（詳見 [kg-status.md](kg-status.md) §4）。

**方向**：新記錄 `name` 改為穩定短 slug（完整句子留 `description`）；既有 14 筆遷移
（改 frontmatter `name` + 若有引用一併改）。

**完成條件**：`ls .claw/memory` 中不存在斷在標點/粗體符號中間的 name；
遷移後 `recall` 對既有 query 的命中不變差（用 memory-usage 帳本裡的高頻 query 抽查 3 條）。

### 進度

- ✅ **產生端已修**（2026-08-31）：`memoryTitle()` 切在句讀邊界並砍掉未閉合括號，
  兩個產生點（新記錄、整併 UPDATE）共用。**只影響往後產生的記錄。**
- ✅ **圖改認檔名 slug**：`Graph()` 同時以 `name` 與 `mem-xxxxxxxx` 索引節點。
  既有 14 筆【立刻可被指向】，不必等遷移——這也讓 #1 的推導邊有永遠穩定的鍵。
- ✅ **既有記錄已遷移**（2026-08-31）：`cmd/ingest -fix-names`（預設預覽、`-apply` 才寫、先備份）。實跑改寫 11 筆，檔名與正文零改動。
- ⬜ **LLM 自擬短標題**（可選）：句讀切法產出的仍是長句片段（如
  「對外部 MCP 工具回傳的資料，一律當作」）。要真正好寫的連結目標，
  得讓反思器自己吐一個 title 欄位——那要改提案格式，範圍較大。

## 3. 🟡 「絕不靜默截斷」入設計慣例

**來源**：他們的規格原話——「Surface the cap in the UI ('showing 24 of M topics') —
**never silently truncate**.」我們剛被同類問題咬過（#2 正是靜默截斷造成的）。

**做法**：一行慣例進 CLAUDE.md（或 DESIGN.md 的慣例節）：
「任何上限（截斷、top-k、頁數）都要在輸出處標明『顯示 N / 共 M』，**絕不靜默截斷**」。

**完成條件**：慣例落檔；並掃一次現有輸出點（技能索引、recall 子圖、metrics 列表）確認
「有上限但沒標」的清單——修不修另議，先讓清單存在。

## 4. ⏸ Inter-agent messaging：觸發條件不變，參考設計records在此

**觸發**（沿用 roadmap，未變）：orchestrator 用出真需求；
`scripts/subagent_briefing_cost.py` 兩條線任一轉紅（累計 ≥50k tokens 或單 session 同型 ≥5 次）。

**觸發時照這份抄**（Hive 的設計，口味與我們一致——檔案、單寫者、原子寫）：

| 機制 | 內容 |
|---|---|
| 傳輸 | 檔案信箱：`agents/<id>/outbox/` 一訊一檔、temp+rename 原子寫、路由器投遞到收件者 inbox |
| 單寫者 | 每個 agent 只寫自己目錄；共享黑板（board.md）只有仲裁者寫 |
| 語意 | FIPA 言語行為子集：只有 `request/query/propose` 有回覆義務，`inform/done` 是終點——防 ping-pong |
| 防活鎖 | `hops` 計數封頂，超限升級仲裁 |
| 去重 | per-agent `cursor.json` 記已處理位置；處理完歸檔 `inbox/.done/` |
| 稽核 | 狀態即檔案、單一 committer 的 git repo |

**完成條件**（觸發後才適用）：兩個具名 agent 能經檔案信箱完成一次 request→done 往返，
且 ping-pong 測試（互發 inform）不產生第三則訊息。

## 5. ⏸ 訂閱包裝：記進 POSITIONING 當已知取捨，不動架構

**觀察**：他們「包 CLI 而非呼叫 API」讓使用者吃現有訂閱（「your existing subscription」），
三個月 5.7k 星驗證需求真實。cogito 自建迴圈直呼 API，吃不到訂閱——**架構層分岔**，
也是之前「能不能用我的 Claude subscription」問題的市場面答案。

**做法**：POSITIONING.md 加一段「訂閱包裝 vs 自建迴圈」取捨（各自的代價：
包 CLI 受制於 CLI 的輸出格式與變動；自建迴圈可控但按 token 計費）。

**完成條件**：POSITIONING.md 有該節，並回鏈本文。**不改任何程式碼。**

---

## 🚫 看過、判斷不追（理由記錄，免得重新焦慮）

| 項目 | 為何不追 |
|---|---|
| Electron 桌面 app | roadmap 已裁決：互動式介面是選配非核心 |
| 12 家 CLI 引擎廣度 | 同「通道廣度」條——廣度是別人的護城河 |
| E2E 加密 P2P | 無跨機需求；其加密聲明未驗證（SECURITY.md 未讀） |
| GOD 仲裁者（policy 放 prompt） | 我們是確定性政策碼＋審批——可測可稽核，這是取捨不是落後 |
| 信封飛行動畫 | 我們是 hub-and-spoke，沒有 peer 訊息可畫；等 #4 觸發再說 |

## 建議執行順序

**#2 → #1 → #3**（#2 是 #1 的依賴；#3 半小時）。#4、#5 有觸發條件／純文件，不占排程。
對照組維度（Hermes → [task-board-research.md](task-board-research.md)、
qm → [qm-learnings.md](qm-learnings.md)）此為第三份。
