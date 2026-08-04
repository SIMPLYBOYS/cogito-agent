# 從 YC qm 抄什麼、不抄什麼（2026-08-04 盤點）

> **狀態：已盤點、未實作。** 這份是「真要動時照著走」的 do-list。
> 對照對象：[yc-software/qm](https://github.com/yc-software/qm)（2026-07-31 開源，MIT，
> TypeScript/Postgres/Fastify，YC 內部用來跑會計/法務/活動/工程）。
> 與 [roadmap-next.md](roadmap-next.md) 的關係：那份是**排序權威**，這份是這批項目的細節。

## 一句話

qm 是 cloud-first、要 Postgres 與平台工程師、甜蜜點 10~500 人公司——**不是競品，是第二個
對照組**（第一個是 Hermes，見 POSITIONING.md）。但它有三處是**架構層可移植、與量級無關**的，
那才是值得拿的。其餘多半是「不同賽道」而非「我們落後」。

---

## 🔴 1. 記憶整併：可 diff 的動作清單（唯一的高價值項）

**qm 怎麼做**（`src/memory/strategies/consolidation.ts`）：記憶是**編號的原子 bullet**，
每條帶 `(YYYY-MM-DD)` 捕獲日期。累積約 10 條新事實後觸發整併，模型只能回四種動作：

```
UPDATE <n>: <改後的事實>
DELETE <n>
ADD: <新事實>
NONE
```

prompt 裡寫死的規則：優先 `UPDATE` 而非 `DELETE`+`ADD`、事實保持原子、移除過時或衍生的、
**使用者明確要求記的絕不刪**、保留 `(said in …)` 出處後綴。整併完在檔尾寫
`<!-- consolidated: YYYY-MM-DD -->`，下次只處理該標記之後的（增量整併）。
`applyConsolidationActions` 負責套用：清舊標記 → 套 UPDATE/DELETE → 追加 ADD（補當日日期）
→ 正規化空白 → 寫新標記。

**我們的現況**：`MemorySynthesizer` 的 `Reflect` / `ReflectFailure` **只會 append 提案**
（`appendProposed`），沒有任何路徑會 UPDATE 或 DELETE **既有的** `.claw/memory/*.md` 記錄。
量的問題靠 `MemoryLoader.Prune(keep)` 的 LRU 歸檔擋，但「舊記憶被新事實推翻」完全沒處理——
兩條矛盾的記憶會同時留在索引裡，模型 recall 到哪條看運氣。

**補法**：新增 `MemorySynthesizer.Consolidate(ctx)`：
1. 讀 `.claw/memory/*.md`，按檔名排序編號（1..n），連同 `description` 餵給 reflect model
   （便宜模型，`COGITO_REFLECT_MODEL` 已有）。
2. 只接受四種動作的行；解析失敗即整批放棄（與現有 evolve 管線的「壞就不動」一致）。
3. **產物寫進提案通道，不直接生效**——`ADD` 走現有 `MEMORY.proposed.md`；
   `UPDATE`/`DELETE` 需要新的提案格式（帶目標檔名 + 舊值 + 新值，人要看得到 diff 才能判斷）。
4. 放行沿用 P1 已做好的逐條審批（`memory list` / `apply memory 1 3` / `reject memory 2`）。

**為什麼這樣切**：整併回的本來就是動作清單，天生適合逐條放行——這是它跟我們既有治理哲學
最合拍的地方，不是另開一套。`DELETE` 尤其**不能自動生效**（記憶刪錯無法從對話還原）。

**驗收**：
- 給三條刻意矛盾的記憶（「用 npm」→「改用 pnpm」→「pnpm 已棄用改回 npm」），
  整併後提案應為 `UPDATE` 而非三條並存。
- `tags: [user]` 的畫像記錄要能被 UPDATE 但**預設不被 DELETE**（對齊 qm 的「使用者要求記的絕不刪」）。
- 提案檔可 review：看得到「動哪一檔、舊值、新值」。
- 整併標記讓第二次跑成為 no-op（冪等）。

**風險**：中。動的是 `.claw/memory/` 的既有記錄（先前所有寫入都是純新增）。
**緩解**：`DELETE` 走 `Prune` 已有的歸檔路徑（移到 `.claw/memory-archive/`）而非真刪，可復原。

---

## 🟡 2. 模型核准清單（多租戶才痛）

**qm 怎麼做**（`resolveRuntimeChoice`，`src/harness/harness-router.ts`）：三層繼承——
org 核准清單 → org 預設 → per-scope 覆寫。關鍵在錯誤處理**分兩路**：

- 使用者**明確請求**未核准的組合 → 丟 `NonRetryableTurnError`（主動越權＝報錯）。
- 繼承來的無效值 → 靜默退回 org 設定（設定漂移＝降級不擋路）。

**我們的現況**：`provider.Configure(model, maxTokens)` 沒有核准清單概念，
per-channel 的 `model <id>` 指令任何人都能把頻道切到任何模型 id。

**補法**：`COGITO_ALLOWED_MODELS`（逗號分隔，未設＝不限，維持現行行為）。
在 `tryModelCommand` 的入口比對；不在清單內回明確錯誤而非靜默忽略。約 30 行。

**為何延後**：單租戶（今天的主要形態）沒有這個問題——能下 `model` 指令的本來就在
`COGITO_ALLOWED_USERS` 白名單裡。**觸發條件**：office 平台實際跑多個非管理員身分時。

---

## 🟡 3. Egress 的「中間態」（只影響 host 模式）

**先更正一個容易搞錯的前提**：我們的 docker sandbox 預設 `--network none`
（`internal/sandbox/docker.go:29`）＝**完全斷網，比 qm 的授權代理更嚴**。
缺的不是「防護」，是**中間選項**。

**qm 怎麼做**（`src/egress-authz-main.ts`）：授權代理，網路可用但每次請求要過閘——
擋 `169.254.0.0/16` / `fe80::/10` / `fd00:ec2::254`；擋 `metadata.google.internal`、
`metadata.goog`（含子網域）；**DNS 解析後再檢查解出來的 IP**（擋 rebinding）；
capability token 走 JWS 或 HMAC-SHA256 常數時間比較 + audience 驗證；沒設 secret 就一律拒絕。

**我們的現況**：兩極——docker 全斷、host（預設）全通。要讓 agent 跑 `npm install`
或打自家 API，今天只能整個放到 host 模式，等於連 metadata endpoint 也一起開放。

**補法（分階段，別一次做代理）**：
1. **先做便宜的**：`COGITO_SANDBOX_NETWORK` 已存在但只是直傳 docker 的 `--network`。
   文件補一句「要網路時用自建 bridge + 防火牆規則」，把選擇權講清楚。這一步零程式碼。
2. 真要做代理再說——那是獨立行程 + token 簽發 + 稽核落地，量級接近 tsnet。

**為何延後**：host 模式的定位本來就是「你自己的機器、你自己負責」，
而需要網路的任務今天多半直接在 host 跑。**觸發條件**：真的要把 sandbox 開給不完全信任的
任務跑（例如 office 平台接外部委託）。

---

## 🟢 4. SECURITY.md：把已有的誠實彙整成一頁

**qm 怎麼做**：`SECURITY.md` 直白列出**不防什麼**，逐字：

> "Command policy is bypassable... obfuscation, encoding, or writing and then executing a script can evade it."
> "Sandbox credentials are plaintext while in use."
> "QM is not a hardened public or multi-tenant service boundary."
> "The agent and software it runs in a sandbox are not trusted to make authorization decisions."

**我們的現況（我原本判斷錯，這裡更正）**：cogito **已經**是誠實的，而且比 qm 具體——
README:36 明寫「不依賴可被繞過的審批」，`docs/incident-blacklist-bypass.md` 更是一份
**帶逐步證據的實際繞過事故記錄**（agent 改寫命令繞過黑名單）＋後續修復。
缺的只是「散在各處、沒有一頁能一次看完」。

**補法**：`SECURITY.md` 一頁，四節：防什麼 / **不防什麼** / 各 posture 的實際保證 /
回報管道。內容全部來自既有文件，不需新結論。約 1 小時，零風險。

---

## ❌ 明確不抄

| 項目 | 理由 |
|---|---|
| `taste` skill 的 22,069 token 負面提示詞 | HN 上被直接罵「a major skill issue」。我們技能走漸進式載入（索引常駐、正文按需 `read_skill`），這點做得比它好——那 22k 是每輪實打實的成本 |
| `filterTapeForAudience` 分觀眾遮蔽 | 我們 `sanitizeDanglingToolUse` 已經在做同一件事的鏡像版（只清 `ToolCalls` 欄位、保留 thinking 文字）。qm 那支解的是「不同人看到不同視圖」，我們沒有那個場景——面板是單一 operator |
| Postgres / 多 harness 適配層 | 不同量級。我們的賣點正是單 binary、近零依賴 |
| "multiplayer" 這個命名 | HN 嫌得很兇：「there's nobody there」 |
| `src/memory/policy.ts` 的記憶政策 | 它只有 scope-based 存取控制，**沒有** size cap、沒有注入/外洩內容檢查。這塊我們比它嚴 |

## 順序建議

**🟢 4 SECURITY.md（1h、零風險）→ 🔴 1 記憶整併（真缺口）→ 其餘等觸發條件**

4 先做是因為它幾乎不花錢又補上唯一的「敘事缺口」；1 是這批唯一真正改變能力的項目，
而且接得上剛做完的 P1 逐條審批（指令表見 [README → Usage](../README.md#usage)）。
2 與 3 都掛在「多租戶/外部委託真的發生」這個條件上，沒發生前做了也驗證不了。
