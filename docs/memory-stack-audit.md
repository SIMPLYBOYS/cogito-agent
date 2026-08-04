# 記憶層自評：對照「AI Agent Memory Stack」七層（2026-08-05）

> **狀態：自評，非規劃。** 這份不是待辦清單——它把 cogito 的記憶實作逐層攤開對照一份
> 流傳中的七層分類，標出**有什麼、缺什麼、哪些是刻意不做的**。待辦在
> [roadmap-next.md](roadmap-next.md)。
>
> 對照的分類來自一份公開流傳的資訊圖「AI Agent Memory Stack — Designing Memory for
> Enterprise Agents」（作者 @rakeshgohel01）。它是個人整理，不是標準；但涵蓋面夠廣，
> 拿來當體檢表有用。

## 先說這套分類本身的問題

七層混了**兩條軸**：

- **01–04（working / episodic / semantic / procedural）** 是認知科學來的分類，
  對應 CoALA 那套（更上游是 Tulving 的記憶分類）。這四層是**內容種類**。
- **05–07** 是後掛的工程關切，而且不是記憶種類：
  - **hierarchical 是放置策略**——熱/溫/冷是「同一批內容放在哪」，不是另一種內容。
  - **shared 是作用域**——誰看得到，不是存什麼。
  - **prospective 勉強算一種**（未來的意圖），但實作上它是排程器不是記憶體。

這不是挑語病，它會直接影響怎麼蓋：**你不會去蓋一個「hierarchical memory 子系統」**，
你是把分層套用在 semantic / episodic 上面。cogito 正好就是這樣做的（見 05）。

---

## 逐層對照

| 層 | 狀態 | cogito 的實作 |
|---|---|---|
| **01 Working**<br>視窗裡裝得下的 | ✅ **超前** | 圖畫的是 naive FIFO 滑窗。我們刻意**離開**滑窗：`EnableSummary` 開時走**錨定式窗口**（全量 + 逐出機制，前綴 append-only），因為滑窗每輪動頭部會讓 prompt cache 輪輪全滅還倒貼寫入費。另有自適應 Compactor（窗口 × 0.75 水位，[compactor.go:12](../internal/context/compactor.go#L12)）、孤兒 `tool_result` 剝除與 `sanitizeDanglingToolUse`（[session.go:155](../internal/context/session.go#L155) 起）。實測數據見 [roadmap-next.md](roadmap-next.md) §1 |
| **02 Episodic**<br>發生過什麼，按需取回 | ⚠️ **有，但不是向量** | [session_search.go](../internal/context/session_search.go) 的 `search_sessions`：關鍵字 + CJK bigram 線性掃描落地的 session，輸出有界（每會話 ≤3 段 × 160 字、預設 5 會話、上限 20）。**不是** embedder→vector store→top-K。刻意的取捨，天花板見下方「真缺口 ②」 |
| **03 Semantic**<br>agent 認定為真的事 | ✅ **最完整的一層** | 離散記錄 `.claw/memory/*.md`（frontmatter name/description/tags）＋知識圖譜（[graph.go](../internal/context/graph.go)：typed 關係、`[[連結]]` 鄰域、k 跳擴張、可選 embedding 選種子）。索引常駐封頂 30 條（[memory.go:19](../internal/context/memory.go#L19)），正文由 `recall` 按需取。`tags: [user]` 的**使用者畫像**正文每輪常駐（[memory.go:24](../internal/context/memory.go#L24)）。圖上那句 "distilled facts, not raw history" 正是這層在做的事 |
| **04 Procedural**<br>怎麼做，寫成技能 | ✅ **且是漸進式** | `.claw/skills/<name>/SKILL.md`（[skill.go](../internal/context/skill.go)），索引常駐、正文按需 `read_skill`（[read_skill.go](../internal/tools/read_skill.go)）。圖裡的 skill library 是靜態的；我們的會**自生成**（Tier 4 反思），但產物只寫 `skills-proposed/`，過 skillgate 結構+危險掃描、人工放行才晉升 |
| **05 Hierarchical**<br>熱/溫/冷，像 OS 分頁 | ✅ **三層齊，只是沒用 OS 話術** | **hot** = System Prompt 常駐（AGENTS.md + 記憶索引 + 使用者畫像）；**warm** = `recall` 按需取回；**cold** = `.claw/memory-archive/`（`Prune` 把最久未用的歸檔，[memory.go:332](../internal/context/memory.go#L332)，**可復原非刪除**）。升降由使用帳本（`memory-usage.json`）的 LRU 驅動，不是自動 page-in/out——app 私有訊號，免疫備份/rsync 碰檔汙染 |
| **06 Prospective**<br>記得接下來要做什麼 | ✅ **四樣全有** | [internal/cron/](../internal/cron/)（scheduler + store + notify + flock 仲裁，每輪 Tick 重讀 `cron.json`，改檔即生效）；背景任務 TaskManager（`bash_background` / `task_output` / `task_kill` / `task_list`，[task_tools.go](../internal/tools/task_tools.go)）；持久目標 `goal` + `goal_paused`；`Running` / `ResumeAttempts` 跨重啟續跑（[session_store.go:30](../internal/context/session_store.go#L30)——行程被硬砍後掃出未完成任務自動接手，有次數上限防崩潰迴圈）。圖上標的 Reminders · Retries · Follow-ups · Autonomy 逐一對得上 |
| **07 Shared**<br>一份真相，所有 agent 對齊 | ❌ **唯一真缺口** | per-agent 記憶只有**讀半邊**：`.claw/agents/<name>/memory/` 在 spawn 時注入子 agent（[subagent.go:169](../internal/tools/subagent.go#L169) 的 `LoadForInjection`），靠手寫填、agent 之間不互見。沒有 state store / knowledge bus、沒有 conflict resolution、沒有跨 agent 一致性 |

**六層有、一層缺。**

---

## 兩個真缺口

### ① 第 07 層（shared memory）——已知、刻意延後

完整解是 inter-agent messaging（大，不做）。中間解在 [roadmap-next.md](roadmap-next.md)
的「具名 agent 的持久記憶」：寫半邊（跑後反思 → per-agent 提案 → 治理放行）。

**觸發條件不變**：orchestrator 實跑確認「專員每次從零開始」真的痛。
**一張分類圖不構成觸發**——圖不是需求。

### ② 第 02 層的天花板：關鍵字抓不到換句話說

`search_sessions` 用 bigram 比對字面。使用者問「上次那個連線爆掉的事」，而 session 裡
寫的是 "pool exhausted" —— **不會命中**。這是關鍵字檢索的結構性限制，不是調參能修的。

**升級路徑很短**：`Embedder` 介面（[embed.go](../internal/context/embed.go)）已經在給
KG 選種子用了，`search_sessions` 接同一個介面即可，`EmbedCachePath` 的向量快取也是現成的。

**為什麼現在不動**：要先有一次**真的檢索失敗**（問了、沒查到、而內容其實在），
再決定值不值得那筆 embedding 成本。沒有那筆證據就上，是為假想需求付費。

---

## 圖裡沒有、但更重要的一層

**七層裡沒有一個框叫 approval。** 所有箭頭都是自動寫入——包括 03 那條把 distilled facts
直接灌進知識圖譜。標題寫著 Enterprise Agents，卻沒有回答「**誰批准這條事實進庫**」。

cogito 的記憶寫入全程走提案通道：

```
反思 → .claw/MEMORY.proposed.md → memory list → apply memory 1 3 / reject memory 2 → .claw/memory/
```

技能同理（`skills-proposed/` + skillgate），知識圖譜的邊同理（`apply edges`）。
YC qm 把記憶整併做成可 diff 的動作清單（`UPDATE <n>` / `DELETE <n>` / `ADD` / `NONE`），
出發點是同一件事——見 [qm-learnings.md](qm-learnings.md)。

**企業級的分野在治理，不在多疊一層儲存。** 一個會自己往知識庫寫入且無人覆核的 agent，
層數再多也只是把錯誤記得更牢。

---

## 怎麼用這份文件

- **不要**照著補第 07 層——它有自己的觸發條件。
- README 目前只寫「對齊 CoALA 長期語意層」，講得比實際保守；真要對外說明記憶設計，
  引這份的表比重寫 README 省事。
- 下次有人拿新的記憶分類圖來問「我們有沒有」，先看這份，再看它是不是又把
  **放置策略**和**作用域**混進**內容種類**裡。
