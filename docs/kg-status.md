# 知識圖譜：現況（2026-08-18 實測）

> 這份寫「**現在是什麼**」，逐項對照過程式碼與這台機器上的實際資料。
> 設計意圖與階段規劃在 [kg-spec.md](kg-spec.md)，兩份不重複。
>
> **一句話**：機制全部做完且**量測證明有效**（`hit@k` 1.00 vs keyword 0.50），
> 但在正式記憶庫裡**目前是空轉的**——14 個節點、**0 條邊**。原因在 §4，是個具體的 bug，不是沒人用。

## 1. 它解的是什麼問題

平面 top-k 檢索撈「字面像的片段」，答不出「A 跟 C 透過 B 連在一起」。KG 把記憶當圖：
**節點 = 記憶記錄，邊 = 記錄之間的關係**，檢索時從命中的節點沿關係走 k 跳，回傳**連通鄰域 + 明確關係**。

## 2. 程式碼落點

| 檔案 | 職責 |
|---|---|
| [internal/context/graph.go](../internal/context/graph.go) | 圖核心：`Edge`、`Graph()` 建鄰接表、`Seeds()` 選種子、`Subgraph()` BFS 擴張 |
| [internal/context/memory.go:532](../internal/context/memory.go#L532) | `RecallGraph(query, hops, emb)`——對外檢索入口 |
| [internal/context/embed.go:95](../internal/context/embed.go#L95) | `SeedsEmbed()`：向量選種子（opt-in，未配置則退回關鍵字） |
| [internal/context/kg_gate.go](../internal/context/kg_gate.go) | 提案邊的把關與併入 |
| [internal/context/ingest.go](../internal/context/ingest.go) | markdown 目錄 → 節點 + 邊（**確定性、不花錢**） |
| [internal/evolve/kg_extract.go](../internal/evolve/kg_extract.go) | LLM 抽 typed 關係 → 寫**提案**邊 |
| [cmd/ingest](../cmd/ingest/main.go) | 操作介面（ingest / 抽取 / 審核 / 併入 / 建向量 / 評測） |
| [internal/tools/recall.go:63](../internal/tools/recall.go#L63) | `recall` 工具實際呼叫 `RecallGraph` |

## 3. 實際行為與參數

### 邊的兩個來源

- **人工**：記錄正文的 `[[target]]` 或 `[[type::target]]`，由 `parseLinks` 解析。指向目標是另一筆記錄的 frontmatter `name`。
- **機器**：`.claw/kg/edges.jsonl`，帶 `type` / `confidence` / `source`。LLM 抽的先進 `edges.proposed.jsonl`，**過閘才生效**。

**懸空連結**（指向不存在的節點）建成 stub 並標 `dangling`——那本身是「該寫還沒寫」的信號，不是錯誤。

### 檢索三步（硬上限寫死在 [graph.go:16](../internal/context/graph.go#L16)）

| 常數 | 值 | 作用 |
|---|---|---|
| `recallSeeds` | 3 | 取幾個入口節點 |
| `recallBudget` | 8 | 子圖總節點上限 |
| `hops` | 預設 1 | 沿關係擴張幾跳（`recall` 工具可傳） |

BFS **無向**擴張（關係兩向都相關），但**輸出保留方向**；就近優先，達 budget 立刻停。
兩層封頂是為了防「三跳之後把整張圖拉進 context」——那會讓 recall 從省 token 變成燒 token。

### 閘的四條規則（[kg_gate.go](../internal/context/kg_gate.go)）

一條 LLM 抽的邊要活下來得同時滿足：

1. `confidence ≥ 0.5`
2. **兩端都是真實存在的節點**，且非自環 ← 幻覺保護
3. 不與既有邊重複
4. 該起點的出邊數 < 8 ← hub 封頂

倖存者併入 `edges.jsonl`，其餘丟棄並回報拒絕數。**機器抽、規則把關、可審計。**

## 4. ⚠️ 現況：機制在跑，但圖是空的

這台機器上的實測（`workspace/.claw/`）：

| 項目 | 數量 |
|---|---|
| 記憶節點 | 14 |
| 正文裡的 `[[links]]` | **0** |
| 生效邊 `edges.jsonl` | **0** |
| 提案邊 `edges.proposed.jsonl` | 9（2026-06-29 產生，**從未審核**） |
| 向量快取 | 無 |

**14 個孤立節點、零邊。** 於是 `RecallGraph` 走完 `Seeds` 之後沒有鄰居可擴張，
`Subgraph` 原樣回傳種子——**行為退化成 top-3 平面檢索**。KG 那一層目前不產生任何效果。

### 根因不是「沒人寫連結」，是節點 ID 不能被連

`[[link]]` 的指向目標是 frontmatter 的 `name`。而 `name` 由
[memory_synth.go:629](../internal/evolve/memory_synth.go#L629) 的 `memoryTitleRunes = 24`
**硬截斷**，14 個節點全部斷在句子中間：

```
涉及數值區間的過濾（如「年薪≥140萬」遇到「1      ← 斷在引號中間
外部工具（MCP/API）查不到或未掛載時，**           ← 斷在粗體標記
處理地址/路名查詢時，先用 GROUP BY 行             ← 斷在詞中間
```

沒有人（或 LLM）寫得出 `[[外部工具（MCP/API）查不到或未掛載時，**]]`。
**節點 ID 是截斷的句子，圖就永遠長不出人工邊。**

那 9 條提案邊的 `from`/`to` 也是這種截斷字串——它們大概率過不了閘的第 2 條
（端點必須是真實節點名），因為抽取時看到的名字跟落盤的名字未必一致。

> 修法方向（未做，需決定）：`name` 改成穩定的短 slug（另存完整句子到 `description`），
> 或讓連結改以檔名/ID 指向而非 `name`。前者動的是新記錄，既有 14 筆要遷移。

## 5. 已量測的效果（在有邊的語料上）

`go run ./cmd/ingest -root <語料> -eval <labels.jsonl> -k 3 -hops 1`，$0、12 秒、不呼叫 LLM：

```
模式                  N    hit@k      MRR
keyword            12     0.50     0.50
embedding          12     0.58     0.53
keyword+kg         12     1.00     0.69
```

語料是 12 個互連服務節點、12 題（半數多跳）。**多跳題的答案節點與查詢字面零重疊**，
keyword 在設計上就撈不到。

補跑 embedding 回答了唯一難以反駁的質疑——「換成向量檢索不就好了？」**不能**：
embedding 只到 0.58，因為多跳題的答案節點**跟查詢在語意上也不像**，它只是**被連到**像的那個。
向量相似度走不了 A→B→C。**贏的是「沿關係擴張」這個機制，不是更好的相似度函數。**

判讀細節見 [eval-results.md](eval-results.md)。

## 6. 操作介面

```bash
# 結構式 ingest（確定性、免費）
go run ./cmd/ingest -src <md目錄> -root <記憶根>

# LLM 抽 typed 關係 → 提案（需 ANTHROPIC_API_KEY）
go run ./cmd/ingest -root <記憶根> -llm

# 審核 → 併入
go run ./cmd/ingest -root <記憶根> -review-edges
go run ./cmd/ingest -root <記憶根> -apply-edges

# 向量快取（opt-in，需 COGITO_EMBED_MODEL + 端點）
go run ./cmd/ingest -root <記憶根> -embed

# 手動檢索一次看子圖
go run ./cmd/ingest -root <記憶根> -recall "查詢字串" -hops 2
```

## 7. 測試覆蓋（真正被保證的行為）

13 條，分佈：

- `graph_test.go`（5）：建邊與懸空節點、typed link 解析、種子與子圖、**budget 封頂**、子圖序列化
- `kg_gate_test.go`（2）：閘的四條規則、**每節點出邊上限**
- `embed_test.go`（2）：cosine 與向量讀寫、embedding 選種子
- `ingest_test.go`（2）：目錄 ingest 出節點與邊、**邊去重**
- `kg_extract_test.go`（2）：**過濾幻覺節點**、節點太少時不抽

沒有測試蓋到的：`edges.jsonl` 實際被 `Graph()` 載入後參與檢索的端到端路徑
（目前圖只從 `[[links]]` 建——見 §8）。

## 8. 沒做的

- **Stage 3b 持久化 / ANN 索引**：每次 `Graph()` 都重建鄰接表。節點數還在兩位數，觸發未到。
- **`edges.jsonl` 併入圖的路徑**：閘會把邊寫進 `edges.jsonl`，但 §7 指出這條沒有端到端測試覆蓋；
  在本機也因為零邊而從未實際跑過。要用之前值得先補一條測試。
- **社群偵測 / 全圖摘要**（GraphRAG 的重型部分）：刻意不做。
- **圖資料庫**：刻意不引——違反單 binary、可審計的定位。

## 9. 下一步（依「動它的風險」排序）

1. **修 `name` 截斷**（§4）——不修的話 KG 在正式庫裡永遠是裝飾。風險低、範圍清楚。
2. **裁決那 9 條提案邊**——跑一次 `-review-edges` 看它們是否還有意義（2026-06-29 至今）。
3. **自動邊（推導，零 LLM）**——co-recall 邊（同 session 一起被 recall）與 provenance 邊（同任務沉澱），讓圖自己長。細節見 [munder-difflin-actions.md](munder-difflin-actions.md) §1。
4. 補 `edges.jsonl` → `Graph()` 的端到端測試（§8）。
5. Stage 3b：等節點數真的讓 `Graph()` 變慢再說。
