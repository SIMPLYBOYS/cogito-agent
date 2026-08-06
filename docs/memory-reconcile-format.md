# 設計：記憶整併的提案格式（2026-08-05）

> **狀態：設計定案，2026-08-05 已實作完成（六步全數落地）。** 這份把「提案通道要怎麼表達破壞性操作」定死，讓後續實作是機械的。
> 動機與對照見 [qm-learnings.md §1](qm-learnings.md)；記憶層全貌見 [memory-stack-audit.md](memory-stack-audit.md)。

## 要解的問題

`MemorySynthesizer` 目前**只會 append**。沒有任何路徑會 UPDATE 或 DELETE 既有的
`.claw/memory/*.md`，所以兩條矛盾的記憶會並存在索引裡，`recall` 撈到哪條看運氣。

補整併不難，難的是**提案通道學不會表達破壞性操作**：現在的提案檔只寫得出「新增這句話」，
寫不出「把第 3 條改成這樣，原本是那樣」。人看不到 diff 就沒辦法審。

---

## 決定 1：不叫 `Consolidate`，叫 `Reconcile`

**`consolidate` 已經被用掉了。** [`internal/tools/consolidate.go`](../internal/tools/consolidate.go)
是讓 agent 主動把當前工作沉澱成**提案**的工具（反思軌跡 → 提案技能／記憶／KG 邊）。

而這裡要做的是**整併既有記憶、消除矛盾**——完全不同的事。沿用同名會讓人分不清
「沉澱新的」和「整理舊的」。

→ 方法名 **`MemorySynthesizer.Reconcile(ctx)`**，提案分類標 **`[整併]`**。

## 決定 2：格式向後相容——動作是 bullet 的前綴，不是新結構

現有格式（`appendProposed`）：

```markdown
## [慣例] 來自任務「裝依賴並跑測試」（2026-08-05T10:00:00+08:00）
- 本專案用 pnpm 而非 npm 裝依賴
```

`parseProposedMemory` 的規則是「`## ` 開頭是區塊標題、`- ` 開頭是一條提案」，編號按掃描順序給
（`N: len(out)+1`）。**這個規則不動**，只擴充 bullet 的內容文法：

```markdown
## [整併] 2026-08-05T14:30:00+08:00（掃過 30 條記錄）
- UPDATE mem-1a2b3c4d — 本專案用 pnpm；CI 也是。npm 已於 2026-07 停用
  舊：本專案用 pnpm 而非 npm 裝依賴
  因：新事實推翻了「只在本機用 pnpm」的暗示
- DELETE mem-5e6f7a8b
  值：Node 14 需要 --experimental-modules
  因：專案已升到 Node 22，此條永遠不會再適用
- 部署前一律先跑 `make verify`
```

文法：

| bullet 開頭 | 動作 | 附帶行 |
|---|---|---|
| `UPDATE <slug> — <新值>` | 改寫既有記錄 | `舊：` 必填、`因：` 必填 |
| `DELETE <slug>` | 歸檔既有記錄 | `值：` 必填、`因：` 必填 |
| 其他任何文字 | **ADD**（現況行為） | 無 |

**為什麼這樣切**：

- **向後相容是白拿的**——沒有前綴就是 ADD，既有提案檔一行都不用改，舊的 `Reflect` 產物照跑。
- **一個 bullet ＝ 一條提案 ＝ 一個編號**，`apply memory 1 3` 的逐條審批直接沿用，不必重寫編號邏輯。
- **附帶行縮排 2 格且不以 `-` 開頭**，舊解析器看到會忽略（不會誤判成新提案），是安全的漸進升級。
- 純文字、無巢狀結構，`memory list` 推到 Slack/Telegram 也讀得動。

## 決定 3：用 slug 指涉記錄，且 **UPDATE 不改檔名**

記錄檔名是 `mem-%08x`——**學習內容的 FNV-32a 雜湊**（`writeMemoryRecord`）。
所以 UPDATE 改了內容之後，「正確的」雜湊也會變。

**仍然保留原檔名。** 理由：使用帳本 `memory-usage.json` 是**以 basename 為 key** 的
（LRU 時間 + 命中次數）。改名等於把那條記錄的使用歷史孤兒化，LRU 排序與 `Prune` 淘汰
會立刻失準。檔名在這裡只是 ID，不是內容的校驗和——讓它退化成純 ID 是比較小的代價。

> 副作用：整併過的記錄，其 slug 不再等於內容雜湊。這只影響「能不能靠檔名反推內容」，
> 沒有任何程式碼依賴那個性質（查過：slug 只在 `writeMemoryRecord` 產生時算一次）。

## 決定 4：三道護欄

破壞性操作要有對應的安全設計，不能只靠「反正要人放行」。

### ① `tags: [user]` 預設不可 DELETE

使用者畫像是**本人明確要求記的**（對齊 qm 的 "never delete facts the user asked you to remember"）。
兩層擋：**提案時**不把畫像記錄列入 DELETE 候選；**放行時**再檢查一次，命中就跳過並回報。

UPDATE 允許——偏好會變，但那要人看著 diff 點頭。

### ② 舊值不符即拒絕（樂觀鎖）

提案產生到人放行之間，記錄可能已經被改過或歸檔了。放行時比對**目前內容**與提案裡的
`舊：`／`值：`（正規化後比字串）：

- 目標檔不存在 → 跳過，回報「已不存在，可能已被歸檔」
- 內容對不上 → **拒絕套用**，回報「記錄已變動，請重新整併」

這是 qm 用 `content_sha256` precondition 做的同一件事。我們的量級用正規化字串比對就夠，
不必引雜湊。

### ③ DELETE ＝ 歸檔，不是刪除

走 `MemoryLoader.Prune` 已經在用的路徑：移到 `.claw/memory-archive/`
（[memory.go:332](../internal/context/memory.go#L332)）。**可復原**。

理由寫在既有註解裡了——記憶操作不可逆的代價太高，刪錯無法從對話還原。

## 決定 5：增量標記放使用帳本，不另開檔

qm 在筆記本尾端寫 `<!-- consolidated: DATE -->`。我們的記憶是**一檔一記錄**，沒有單一筆記本
可以寫標記；而提案檔會被放行消耗掉，也不能放那裡。

→ 存進既有的 `.claw/memory-usage.json`（app 私有 sidecar，已經有原子寫與鎖），
多一個 key：`{"_reconciled_at": "2026-08-05T14:30:00+08:00"}`。

不另開檔案的理由就是不想再多一個要維護的狀態檔。前綴 `_` 與記錄 basename 天然不衝突
（basename 一定含 `.md`）。

## 決定 6：`memory list` 的呈現

檔案是真相且可以囉嗦（保留完整舊值），聊天視圖要精簡：

```
待審提案記憶（3 筆）

1. [慣例] 本專案用 pnpm 而非 npm 裝依賴
2. [整併·UPDATE] mem-1a2b3c4d
     舊：本專案用 pnpm 而非 npm 裝依賴
     新：本專案用 pnpm；CI 也是。npm 已於 2026-07 停用
     因：新事實推翻了「只在本機用 pnpm」的暗示
3. [整併·DELETE] mem-5e6f7a8b  ⚠️ 會歸檔（可復原）
     值：Node 14 需要 --experimental-modules
     因：專案已升到 Node 22

放行：apply memory 2 3    丟棄：reject memory 1
```

破壞性的那條要有 `⚠️` 與「可復原」的說明——審的人得知道按下去會發生什麼。

---

## 給模型的動作文法（prompt 契約）

沿用 qm 驗證過的四動作，規則翻成我們的脈絡：

```
UPDATE <slug>: <改後的事實>
DELETE <slug>
ADD: <新事實>
NONE
```

prompt 內寫死的規則：

- **優先 UPDATE 而非 DELETE+ADD**（保住使用歷史與 provenance）
- 事實保持原子——一條講一件事
- 只在**確實矛盾或確實過時**時才動；相似不等於重複
- **`tags: [user]` 的記錄不可 DELETE**
- 每個 UPDATE/DELETE 都要給「因：」，那是人審的依據
- 沒有要動的就回 `NONE`

**解析失敗即整批放棄**，與既有 evolve 管線一致（`extractJSON` 失敗就不動）。

## 觸發點

qm 是「累積約 10 條新事實後」。我們有三個現成掛點，建議**先只做手動**：

| 掛點 | 建議 |
|---|---|
| 手動指令 `memory reconcile` | ✅ **先做這個**——可控、可觀察、零意外 |
| post-task hook | ❌ 每次任務後跑 LLM，成本與雜訊都不划算 |
| `consolidate` 工具 | ⏸ 之後可加，但要先確認手動版產出的品質 |

---

## 實作順序（皆在 `internal/evolve/`）

1. `ProposedMemoryEntry` 加欄位：`Op`（`add`/`update`/`delete`）、`Target`、`Old`、`Why`
2. `parseProposedMemory` 擴充：認 bullet 前綴 + 吃縮排附帶行（**不動編號規則**）
3. `Reconcile(ctx)`：讀記錄 → 編號 → 餵 reflect model → 解析四動作 → 寫提案
4. `ApplyProposedMemory` 擴充：`update` 改寫、`delete` 歸檔，含三道護欄
5. `memory list` render + `memory reconcile` 指令
6. 使用帳本的 `_reconciled_at`

**測試要蓋的**：向後相容（純 ADD 提案檔解析結果不變）、三道護欄各一條、
UPDATE 不改檔名、DELETE 進得了 archive、壞格式整批放棄、第二次跑是 no-op。

**規模**：中等。多數成本在 2 與 4，不在 3——**「加一個 LLM 呼叫」是最小的那部分**。

---

## 實作後記（2026-08-05）

六步全部完成。與設計相符，但有三處實作時才發現的調整：

1. **模型介面改用 JSON**，不是設計裡寫的行文法（`UPDATE <n>: …`）。四動作語意不變，
   但 JSON 能複用既有 `extractJSON`+`Unmarshal`，且事實內容含冒號時不會把解析弄壞。
   **檔案格式仍是人類可讀的動作 bullet**——那是給人審的，兩者不必同一套。
2. **`ApplyProposedMemory` 加了 `skipped` 回傳值**。設計只寫了「拒絕並回報」，沒說回報去哪。
   破壞性操作被擋下時若沒有回饋，使用者按了 `apply memory 3` 會看到「沒動靜也沒解釋」，
   比直接失敗更糟。被擋的條目留在提案檔，並**依原編號排回去**——否則下次 `apply memory 3`
   指到的是別條。
3. **增量標記改看檔案 mtime**，不是使用帳本的 `usedAt`。帳本記的是「最近被 recall」而非
   「最近被改」。帳本之所以不信 mtime（見 `seedMissing`）是因為備份/rsync 會讓【淘汰決策】
   反掉——代價很高；這裡誤判只是多跑一次 LLM 呼叫，方向還是安全的（寧可多整併，別漏矛盾）。
   `_reconciled_at` 仍存在帳本裡（如設計）。

順手抽出兩個常數，都是「同一規則在兩處各寫一份」的既有隱患：`proposedFileHeader`
（提案檔警語，逐條放行會重寫整份檔案，兩邊分岔就會悄悄換掉警語）、`memoryTitleRunes`
（短標題截斷長度，整併的 UPDATE 要套同一規則）。
