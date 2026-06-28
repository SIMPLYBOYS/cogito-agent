# Spec：知識圖譜記憶（Knowledge-Graph Memory）

> **狀態**：設計中、未實作。本文是可迭代的前瞻設計。Stage 1 待開工；Stage 2/3 有明確觸發條件才做。
> 背景與取捨見 [DESIGN.md](../DESIGN.md) 的「長期記憶」維度；現有記憶實作見 [internal/context/memory.go](../internal/context/memory.go)。

## 目標
把「離散記錄 + 關鍵字檢索」升級成「**可遍歷的圖 + 子圖檢索**」，讓 agent 能做 **RAG 做不到的多跳關係推理**；並支援**多文件 md ingest** 成圖。核心理念：**KG 提供結構，LLM 在明確的關係子圖上做推理**。

為什麼是 KG 而非 RAG：平面相似度檢索撈「相關片段」，但答不出「X 與 Y 透過什麼連、沿關係多跳會到哪」。對長期累積記憶、多文件交叉引用、以及**自我進化（撈對的慣例/教訓）**，關係結構是乘數。代價是：**KG 的威力完全取決於抽取品質**，故護欄是設計重點（見 §6）。

## 1. 圖模型（複用現有結構，不重建）
| 元素 | 落點 | 說明 |
|---|---|---|
| **節點 node** | `.claw/memory/<slug>.md` | id = frontmatter `name`（也是 `[[name]]` 的指向目標） |
| **邊 edge（人工）** | 正文裡的 `[[name]]` | 有向：A 正文含 `[[B]]` → A→B，型別預設 generic |
| **邊 edge（機器，Stage 2）** | `.claw/kg/edges.jsonl` | 帶 `type/confidence/source` 的 typed edge，LLM 抽取產出 |
| **label** | frontmatter `tags` | 節點型別/分類 |

- 圖**載入時在記憶體建鄰接表**（out/in 雙向索引）；數千節點足夠。`// ponytail: 巨量時才做持久化圖索引`。
- **懸空連結** `[[X]]`（無此節點）：建成 **stub 節點**（只有 name、標 `dangling`）——本身是「該寫但還沒寫」的信號；retrieval 可選擇是否納入。

## 2. 邊的兩個來源（分階段）
- **Stage 1（人工/既有）**：只解析正文 `[[links]]`，regex `\[\[([^\]]+)\]\]`。零成本、零依賴、複用現有記錄。
- **Stage 2（機器/ingest）**：LLM 從文字抽 **typed 關係**（如 `depends-on` / `contradicts` / `part-of`）→ 寫進 `edges.jsonl` 的**提案區** → gate 審核 → 才併入。typed edge 正是 KG 勝 RAG 的來源，且走既有 propose→gate→apply。

## 3. 子圖檢索（核心演算法）
`recall(query, hops, budget)`：
1. **選種子**：用既有 `scoreRecord`（關鍵字/bigram）對所有節點評分 → 取 top-S 種子。（Stage 3 可換 embedding 選種子＝混合）
2. **擴張**：從種子 BFS **無向**走 k 跳（關係兩向都相關），但**輸出保留方向**。
3. **封頂**：總節點 ≤ budget，就近優先（近跳先納）；超過即停（防 context 爆炸）。
4. **序列化**回傳：納入節點的正文 + 一段「關係」列出子圖內的邊（`A —depends-on→ B`）。LLM 拿到的是**連通鄰域 + 明確關係**，不是孤立 chunk。

## 4. recall 工具升級（向後相容）
- 仍是 `recall`，但回傳**子圖**而非平面 top-k；新增可選 `hops`（預設 1）。
- agent 可對鄰居 name 再 `recall` 往外走（多跳由多次呼叫或一次 k 跳達成）。

## 5. 多文件 md ingest 路徑
- 入口 `cmd`/工具：`ingest <dir>` → 每個 .md：
  - **結構抽取（確定性）**：檔→節點（name=標題/slug、tags=frontmatter）、`[[links]]` 與標題層級→邊。免費、立即可用。
  - **LLM 關係抽取（可選，gated）**：抽隱含 typed 關係 → `edges.jsonl` 提案 → 審核。
- ingested 節點進**同一 `.claw/memory/` 庫**（統一一張圖），但標 `source:ingest` 以區分（Prune/歸檔可分別對待）。

## 6. 抽取品質護欄（KG 的成敗在此）
- LLM 抽的邊**一律提案、不自動生效**（gated，同自我進化）。
- **去重**（same subject-type-object 收斂）、**每節點邊數封頂**（防 hub 爆炸）、**指向不存在節點的邊**丟棄或建 stub 標記。
- LLM 邊帶 **confidence + provenance**（來自哪份文件），低信心丟棄或標註；可審計。
- 檢索端：`hops` / `budget` 雙封頂，防子圖爆 context。

## 7. 介面（Go，新增 `internal/context/graph.go`）
```go
type Edge struct{ From, To, Type string } // Type "" = generic [[link]]
type Graph struct { /* nodes map[name]MemoryRecord; out,in map[name][]Edge */ }

func (m *MemoryLoader) Graph() *Graph                          // 從記錄 [[links]] 建圖
func (g *Graph) Seeds(query string, n int) []string            // 複用 scoreRecord
func (g *Graph) Subgraph(seeds []string, hops, budget int) ([]MemoryRecord, []Edge)
func RenderSubgraph(nodes []MemoryRecord, edges []Edge) string  // 序列化給 LLM
```
recall 工具改用 `Graph().Seeds → Subgraph → RenderSubgraph`。

## 8. 分階段（各有觸發）
1. **Stage 1（先做）— 把現有記錄當圖**：`graph.go`（解析 `[[links]]` + 鄰接表）+ recall 改子圖檢索。**最小、純複用、立刻有多跳。不含 ingest、不含 LLM。**
2. **Stage 2（觸發：開始多文件 ingest）**：`ingest` 路徑 + typed `edges.jsonl` + gated LLM 關係抽取。
3. **Stage 3（觸發：種子不準 / 巨量）**：embedding 選種子（混合）、持久化圖索引。

## 9. 與既有怎麼接
- **memory**：節點就是 `.claw/memory/*.md`；`recall` 升級、`LoadIndex`/`Prune` 不變。
- **synth/gate**：LLM 抽的邊走 `edges.jsonl` 提案 + gate，複用 propose→review→apply 與安全掃描精神。
- **DESIGN.md**：「長期記憶」維度的 KG 取捨指向本文。

## 10. 刻意不做（YAGNI）
- **不引圖資料庫**（Neo4j 等）——違反單 binary / 可審計。
- **不做社群偵測 / 全圖摘要**（GraphRAG 的重型部分）——需求未到。
- **Stage 1 不碰 embedding / LLM**——先用明確 `[[links]]` 把圖跑起來。
