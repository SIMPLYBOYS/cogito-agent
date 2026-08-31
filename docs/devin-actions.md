# Action Items：對照 Devin（2026-08-31）

> 來源：docs.devin.ai 六頁官方文件（knowledge / creating-playbooks / dynamic-workflows /
> ai-guardrails / automations / 文件索引）。**官網與定價被 429 擋下沒讀；DeepWiki 未抓細節；
> 全部來自文件、沒實際用過。** guardrails 文件未說明是確定性還是 LLM 判定。
>
> 第四份對照研究（Hermes → [task-board-research.md](task-board-research.md)、
> qm → [qm-learnings.md](qm-learnings.md)、Munder Difflin →
> [munder-difflin-actions.md](munder-difflin-actions.md)）。判準沿用：按「動它的風險」排序，
> 觸發條件擋著的不動，[POSITIONING.md](../POSITIONING.md) 裁決過的量級差異不重新焦慮。

---

## 1. 🟢 記憶條目的「觸發描述」與內容分離

**是什麼**：Devin 的知識條目 = **trigger description + content** 兩個欄位——
「什麼情況該想起我」與「我說什麼」分開，檢索比對的是觸發不是內容。

**為什麼值得**：這是我們**技能索引早就在用的原則**（description = 何時用），但記憶層沒有：
`description` 是內容摘要，`scoreRecord` 比對內容字面。直接對上已量測的弱點——
**14 筆記憶 11 筆 `hits: 0`**。「換算每坪乘 3.305785」的觸發時機是「使用者問房價/坪數」，
但那幾個字不在內容裡，關鍵字比對天生撈不到。

**做法**：frontmatter 加選填 `trigger:`；`scoreRecord` 給它最高權重（高於 tags 的 4）；
`reflectSystemPrompt` 的記憶側要求反思器順帶產出。既有記錄不回填（要 LLM 或人工，另議）——
`trigger` 缺省時行為與現在完全相同，向後相容。

**規模**：小～中。

**完成條件**：一條「內容不含查詢詞、但 trigger 含」的測試記錄能被 recall 撈到
（現況撈不到，即紅→綠）；缺 `trigger` 的舊記錄檢索行為不變。

### ✅ 已完成（2026-08-31）

完整鏈路：反思句尾「｜觸發：…」 → 提案 bullet 的「觸發：」續行（與 舊/因 同文法）→
解析 → 放行 → 記錄 frontmatter `trigger:` → `scoreRecord` 權重 6（高於 tags 的 4）。
兩條完成條件都以紅→綠驗證；另補 round-trip 測試釘住五個接點——實作時它就抓到一個真缺口
（解析端刻意忽略 ADD 的縮排續行，為 trigger 開了唯一的白名單口、原防護保留）。
既有記錄不回填，缺省行為不變。

## 2. 🟡 技能模板補兩段：Required from User、Forbidden Actions

**是什麼**：Devin playbook 六段式裡我們缺的兩段——
「**Required from User**：這件事需要使用者先給什麼（agent 自己拿不到的）」、
「**Forbidden Actions**：絕對不可做的事」（獨立段落，而非散在散文裡）。

**為什麼值得**：前者讓 agent **開場先要**，而不是跑到一半卡在缺憑證；
後者給禁令一個結構化位置（gate 的 `negationPattern` 已保護警告句不被誤擋，缺的只是格式）。
其餘四段我們已等價（Specifications ≈ 完成條件，來自 mattpocock 那輪）。

**做法**：`reflectSystemPrompt` 的 body 模板加兩個**選填**段落（「沒有就不要硬寫」——
空段落是雜訊）。**規模**：幾行。

**完成條件**：模板落檔。不加測試——提示詞字串斷言是套套邏輯（同 d10f5a3 的判斷）；
真正的驗證是下一輪 retrospect 的提案長什麼樣。

### ✅ 已完成（2026-08-31）

兩段以【選填】加進 reflectSystemPrompt 的 body 模板（「有真實內容才寫，空段落是雜訊」）。
實作時揪出一個閘相容性的坑並已在模板內警告：Forbidden actions 天生會寫禁止句，而
dangerousSkillPatterns 掃全文、無否定句豁免——實測「不要跑 rm -rf 清目錄」一句就讓
技能過不了晉升。模板明講「寫行為描述，不要貼危險指令原文」。

## 3. ⏸ 子 agent 呼叫記錄與重放（可續跑的編排）

**是什麼**：Devin Dynamic Workflows 的核心機制之一——
「Interrupted runs **replay completed agents instantly from recorded results**」。

**對應的洞**：我們主迴圈有跨重啟續跑（`ResumeAttempts`），但 **orchestrator 掛在第 4 個
子 agent 時，前 3 個的成果就丟了**——重跑全額重付。

**觸發條件**（到了才做）：實際發生「orchestrator 跑到一半掛掉、重跑成本 ≥ $1」**兩次**。
現在的 orchestrator 任務都是分鐘級，重跑便宜，做這個是解還沒有的痛。

**觸發時的參考設計**：`(agent_type, task_prompt 內容雜湊) → result` 落盤（同 session 目錄）；
重跑遇到相同鍵直接重放。鍵含內容雜湊所以 prompt 改了就不會誤中——與 `memSlug` 同一個
內容定址思路。注意 Devin 的告誡：**確定性要求**（prompt 不得含時間戳/隨機值，否則永不命中）。

**順帶記錄**：他們把編排整個搬進**確定性腳本**（模型寫腳本、腳本派 agent）——比我們更貫徹
我們自己的原則 3。任務量級到了值得重看，現在不動。

## 4. 📝 兩個只記不做的觀察

- **分級守則回應**：Devin guardrails 有 log_only / warn_user / block 三級；我們只有二元 deny。
  現在的信任模型（authz 白名單）用不到，若哪天接受較不受信的輸入源再考慮。
- **不受信觸發源的網路政策**：他們對 webhook 觸發的 session 單獨限制外連。
  哪天做事件觸發（見下表），這條要**一起**來，不是之後補。

---

## 🚫 判斷過不做（量級差異，POSITIONING 已裁決）

| 項目 | 為何不追 |
|---|---|
| 雲端 VM / 企業面（RBAC、SSO、SCIM、IP 白名單、ACU 計費） | 有資金平台的產品面；我們是單 binary 自託管利基 |
| MultiDevin 規模編排 | 同上；我們的 orchestrate + 並發上限 3 是刻意的閘 |
| 事件觸發廣度（GitHub CI 失敗/Linear/webhook → 開任務） | **真缺口但屬廣度**——roadmap 已定調是 transport adapter 的事，按需求加；加的那天帶上 §4 第二條 |
| DeepWiki / repo 索引 | **未評估**（沒讀到細節），不是「不追」——誠實區分 |

## 建議執行順序

**#1 → #2**（#1 打在已量測的弱點上；#2 幾行）。#3 掛觸發、#4 純記錄。
