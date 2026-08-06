# 下一批 Action Item（2026-07-22 盤點；07-24 更新）

面試前刻意停手的東西，集中在這裡。**排序依據是「動它的風險」而不是價值**——會被延後的，
多半是因為改動面敏感，不是因為不重要。

> **07-24 現況**：面試（07-23）已結束。三條已結案（皆帶實測證據）：✅ 1 caching 斷點③、
> ✅ 2b 政策拒絕＝目標終止、✅ 3 artifact `get`。剩餘見文末「建議順序」。

> 現況基準：CI 四關綠、23 個套件通過。詳細數據見 [eval-results.md](eval-results.md)、
> demo 腳本見 [demo-runbook.md](demo-runbook.md)。

---

## 🔴 動核心路徑（改壞是全面失效，不是單一功能失效）

### 1. ✅ Prompt caching 斷點③（對話尾端）——已完成（2026-07-24）

三件事一起落地（缺一不可）：**斷點③**（buildParams 在最後一則訊息的最後一個 block 掛
ephemeral；`≥3 則`才掛——一次性呼叫如 evolve 反思/judge 掛了是純 1.25x 寫入稅）；
**錨定式窗口**（EnableSummary 開＝主迴圈吃全量，history 由逐出機制有界、序列 append-only
前綴才穩定；滑窗每輪動頭部會讓對話快取輪輪全滅還倒貼寫入費）；**入口對齊**（operator chat
與 claw-cli 先前漏接 EnableSummary、一直走滑窗，已補——.env.example 本來就寫「對話式入口預設開」）。

**實測（同一面板多輪任務，前後對照）**：
修復前快取讀死在 1414（靜態前綴）、全價輸入 645→3908→4200 一路長大；
修復後快取讀 6905→7154→7277→7398 逐輪成長，**全價輸入每輪只剩 2 tk**，寫入僅增量（~100-250 tk）。

已知可接受的 miss（刻意不修）：摘要 commit 輪（system 換摘要，每 ~20 輪一次）、
Plan Mode 打勾輪（進度錨在 system）。原始分析見 vault
`cogito-agent-Context-成本結構實測-Prompt-Caching-的覆蓋缺口`。

結構圖（三斷點 + 錨定窗口 + 前後對照）：[`docs/diagrams/caching-breakpoints.svg`](diagrams/caching-breakpoints.svg)
（原始檔 `docs/diagrams/caching-breakpoints.drawio`）。

<details><summary>原始分析（2026-07-22）</summary>

**現況**：`internal/provider/claude.go:143,150` 兩個 ephemeral 斷點，蓋住 `tools + system` 前綴。
`params.Messages`（對話歷史）**一個斷點都沒有**。

實測（SWE-bench opus 生成的前三次呼叫）：

```
call 1   輸入  645 tk   快取讀     0 / 寫 1414
call 2   輸入 3908 tk   快取讀 1414 / 寫 0
call 3   輸入 4200 tk   快取讀 1414 / 寫 0
                ▲              ▲
         一路長大         永遠固定在 1414（靜態前綴）
```

**對話增量每輪以全價重送。** 補法：每輪在最後一則訊息掛斷點（Anthropic 上限 4 個），
下一輪整段對話成為可命中前綴。以 operator session 的 ~10.8k tk 歷史估算，長任務後段
可省八成以上輸入費用；TTL 5 分鐘，ReAct 每輪隔幾秒必然熱。

**為何延後**：改的是 `buildParams`，所有 LLM 呼叫的必經之路。
**重啟條件**：任何一次長任務的成本明顯刺眼時。
**完整分析**：vault `cogito-agent-Context-成本結構實測-Prompt-Caching-的覆蓋缺口`。

</details>

### 2. SWE-bench 補 `-swe-env-setup`（驗證那個推測）

**觀察**：`MaxTurns=40`，但五題中**四題在 3~5 回合就自己收手**（不是被切斷）。
唯一花到 19 回合的那題，正好是有東西可探索的。

**推測（未驗證）**：env setup 空著 → repo 沒裝依賴 → **跑不了測試 → 沒有回饋訊號可迭代**，
agent 只能讀 issue、讀幾個檔、寫 patch 就無事可做。

**若成立，提分的最大槓桿是備妥測試環境，不是換更強的模型。** 這個結論本身比多跑 25 題有價值。

---

### 2b. ✅ 政策拒絕應為「目標終止」——已完成（2026-07-24）

`ToolResult.Denied` 旗標：Deny／無人值守 fail-closed 標之→引擎終止該目標（觀察先落
history、不注入救援指南）；子 agent 經 `tools.ErrPolicyDenied` sentinel 上傳同樣終止；
**人工拒絕（HITL）刻意不標**（人在場可引導改道）。同場景實測複驗：修復前繞過刪檔、
修復後拒絕即終止、檔案完好。詳見 incident-blacklist-bypass.md 末節。原始分析留檔如下。

<details><summary>原始分析（2026-07-22）</summary>

### 政策拒絕應為「目標終止」，而非可重試的工具錯誤 — **預演實測發現**

2026-07-22 預演 demo ② 時實測：無人值守政策**成功拒絕** `rm -rf scratch/build`，
但 agent 讀到拒絕訊息後**主動改寫命令繞過**，並完成刪除：

```
被拒： rm -rf scratch/build
改用： cd scratch/build && rm app.bin app.map && cd .. && rmdir build
```

它在回應裡明講「我改用不觸發黑名單的方式刪除」——不是被騙，是為了完成任務。
逐步證據：[incident-blacklist-bypass.md](incident-blacklist-bypass.md)。

**兩個層次的問題，第二個更重要：**

1. **黑名單抓錯了東西**（已知，程式碼註解早有記載）——在命令字串上做模式比對，同時會誤攔
   合法操作、又擋不住有動機的繞過。此例中 `rm -rf` 刪的是 agent 自己工作區裡的建置產物，
   **本來就是合法操作**；真正的邊界（`resolveInWorkDir`、Docker 掛載範圍）從頭到尾沒被挑戰。
2. **拒絕的語意錯了** ← 這才是該修的。政策拒絕目前以**工具錯誤**的形式回到 ReAct 迴圈，
   agent 把它當成「換個方法再試」的回饋。對安全政策而言，Deny 或許該是**該目標的終止**
   （回報給人、結束本輪），而不是可重試的錯誤。

**為何延後**：改的是 `policy.Guard` 的回傳語意與引擎對它的處理——動到主迴圈的錯誤路徑。
**重啟條件**：面試後立即評估；這是目前清單上**唯一由實測暴露的真設計缺陷**。

</details>

---

## 🟡 新功能（獨立模組，不碰既有路徑）

### 3. ✅ Artifact 取回：`get` 指令 —— 已完成（2026-07-23）

兩半都落地：**bot** `get <路徑>`（Telegram `sendDocument` multipart／Slack files 三段式上傳，
上限 50 MB，路徑走 `ResolveInWorkDir` 限該頻道 workdir）；**面板** run 頁每任務尾列
「本次產出」檔案清單（含 implementer 類子 agent 內部的寫入，`replay.collectArtifacts`）。

守住的鐵律：**user-pull only**——`fileSenders` 只有 `get` 指令會呼叫，agent 沒有上傳工具
（agent-push 是 prompt injection 的外滲通道）。測試：`get_cmd_test.go`（逃逸/目錄/缺檔/
無 sender/成功路由）、`artifacts_test.go`（主+子 agent 收集、去重）。

### 3b. ✅ Telegram 論壇主題（forum topics）路由 —— 已完成（2026-07-24）

`chatKey` 把 `message_thread_id` 摺進原生頻道 key（`chat:thread`）交給 Dispatch——convID／session／
工作目錄自然各自獨立；`send`/`sendFile`/`postMessage` 用 `parseChatThread` 還原並帶回 `message_thread_id`，
回覆落回原主題。**判準用 `is_topic_message`（比原 plan 的 `!=0` 更準）**：一般群組的回覆串（有
thread 但非主題）不分家，否則普通群組會被回覆鏈拆成一堆 session。負的超級群組 id 內含 `-` 不含 `:`，
namespace 的 Cut（只切第一個冒號）與 thread 的 Cut 都不誤切。測試：`thread_test.go`
（chatKey 五情境／parseChatThread 含負 id／收→namespace→送往返）。

<details><summary>原始分析（2026-07-22）</summary>

**現況**：`internal/telegrambot/bot.go` 只取 `Chat.ID` 當 convID，`message_thread_id` **完全沒解析**；
`postMessage` 也沒帶回。後果：**一個群組裡的所有主題塌成同一個 session** —— 五個專員共用一份
對話狀態與工作目錄，且回覆會掉到群組主層而不是原主題。

**為何在意**：Hermes Mission Control 的教學用「Create the Group & Let the Bot In」（單數 bot）＋
「Capture Each Channel's Thread ID」——推測是**一個群組開 Topics、一個 bot、依 thread_id 路由到
不同 profile**（該教學為付費內容，公開段落未含實際 config，此為線索推測非查證）。
那是「一個專員一個區」這類 UX 的最小前提，而且比多實例便宜一個量級。

**改法**（約 30–50 行 + 測試）：

```go
// 收：thread 併進 convID —— convID 一變，session key 與工作目錄就各自獨立
convID := chatID
if m.MessageThreadID != 0 {
    convID = chatID + ":" + strconv.Itoa(m.MessageThreadID)
}
// 送：postMessage 要帶 message_thread_id 回去，否則回覆掉到主層
```

需同步改：`tgMessage` 加欄位、`send`/`postMessage` 簽章帶 thread、convID 解析（送訊時要能從
convID 還原出 chatID + threadID）。注意 `sanitizeSegment` 已會把 `:` 換成 `_`，工作目錄命名不受影響。

**優先度**：若 P0 試跑 orchestrator 後想要「每個專員在 Telegram 有自己的區」，**先做這個、
不要先做多實例**——多實例會直接掉進 inter-agent messaging 的大坑。

</details>

### 4. C-Auth 走 tsnet

`WhoIs(r.RemoteAddr)` 從 **WireGuard 的密碼學保證**拿身分，全程無 HTTP header 參與——
與 Serve 的 identity header 路徑本質不同（後者繞過 Serve URL 直連即可偽造）。
改動＝`http.ListenAndServe` 換成三行 + 一層 middleware，**其餘 handler 不動**。

它消解了現有 spec 的多個問題：反代讓「loopback=安全」失效的 footgun 不存在、fail-closed
守衛無事可守、「loopback 無身分」的前提不成立、「只有雲上實測能驗」不成立（tailnet 內兩台
裝置即可）。

**代價**：依賴 Tailscale 控制平面、binary 膨脹（故 dashboard 要獨立 binary）、state dir 要管、
`WhoIs` 回傳在某些司法管轄區屬個資。
**待驗證 5 項**見 vault `cogito-agent-Operator-Dashboard-C-Spec` §八。

**📋 分 Phase 的 Action Plan**（真要動時照著走）：[docs/tsnet-plan.md](tsnet-plan.md)
——Phase 0 spike 去風險 → Phase 1 依賴隔離決策（傾向獨立 nested module 保住精簡 go.sum）→
listener+WhoIs middleware（與 #5 匯流：稽核身分改用 WhoIs）→ 真 tailnet 兩裝置驗證。
**觸發條件**：面板要給本機以外的人用／多實例遠端聚合／上雲——在那之前 loopback + SSH tunnel 已足夠。

### 5. ✅ 面板讀反向代理注入的身分標頭 —— 已完成（2026-07-24）

`operatorID` const 保留為預設；新增 `operatorIDFrom(r)`：有 `X-Forwarded-User` 就用它（截長 120 +
濾控制字元防 log/JSON 注入），沒有就退回 `"dashboard(operator)"`。兩處稽核寫入（ApprovePair /
Revoke）改用它。稽核粒度從「從面板做的」升級成「aaron@example.com 做的」——**當且僅當**前面有會
覆寫此標頭的可信代理；否則零行為改變。測試 `authzpage_test.go`（有/無標頭、清洗、截長）。

**這不是認證**（程式碼註解已明載）：純 loopback 直連者可偽造標頭，但那就是機器主人本人，署名自己
的稽核而已。硬信任鏈仍是 #4 tsnet（`WhoIs` 從 WireGuard 拿身分，無標頭可偽造）——兩者不衝突。
**部署備註**：現在跑純 loopback，尚無代理，此升級待實際擺一個反代（oauth2-proxy / tailscale serve）
在前面才顯效。

---

## 🟢 評測補完 —— **四項全數結案（2026-08-05）**

| 項目 | 現況 | 該做到 |
|---|---|---|
| ✅ **skill-ab 樣本數** | ~~1/5→4/5、p=0.206~~ → **n=20 完成（2026-08-05）：7/20→15/20、p=0.0248 達顯著**，花費 $0.65 | ~~n ≥ 20~~ **已結案** |
| ✅ **A/B 樣本數門檻** | ~~跑一次就輸出結論~~ → **已對齊 `evolve.MinVerifySamples`（2026-08-05）**：n<10 時樣本門檻【先於】p 值，不論 p 多小都印「樣本不足」 | **已結案** |
| ✅ **SWE-bench opus 分數** | ~~評測叫停無分數~~ → **官方 harness 補跑完成（2026-08-05）**：opus resolved **4/5**、haiku 1/5、errors 0。但 **p=0.206 未達顯著**，仍是觀察 | ~~跑完官方 harness~~ **已結案**（要當結論需 n≥20，約 $4.9） |
| ✅ **embedder 配置** | ~~N=0 被跳過~~ → **補跑完成（2026-08-05，bge-m3/Ollama）**：keyword 0.50 → **embedding 0.58** → keyword+kg 1.00 | **已結案**——順帶回答了「換向量檢索不就好了嗎」：不行，贏的是沿關係擴張的機制 |

> **這一輪共花 $0.71（全在 skill-ab），其餘三項零 API 成本**——SWE-bench harness 是本地
> Docker、embedding 走本機 Ollama、樣本門檻純程式碼。
>
> 三件事順帶落地：
> 1. **顯著性檢定進了工具**（`internal/eval/abstats.go` + `-ab-n`）。先前 p=0.206 是手算的，
>    跨 20 次 stdout 手動計票只會再犯一次同類方法論錯誤。
> 2. **樣本門檻先於 p 值**：n<10 一律印「樣本不足」，避免小樣本碰巧顯著給出假安心。
> 3. **這把尺立刻反過來管我們自己**——SWE-bench 的 opus 4/5 vs haiku 1/5 是同一張
>    p=0.206 的表，所以那條照樣只能寫成觀察，不能寫成「pass@1 80%」。
>
> 剩下唯一還想補的：SWE-bench n=5→20（約 $4.9），但那要先做 #2 `-swe-env-setup`
> 才知道是不是在測一個被環境卡住的 agent。

---

## ⚪ 已知天花板（等觸發條件，不是待辦）

- **技能索引／MCP 目錄無上限全載**：twinkle-hub 一家 52 支佔現有 66 支的八成。
  該做的是「按任務語意粗篩再列」，而不是逐個關 server（實測關掉 job104+everything 只省 686 tk，
  且那些 token 多數時候以 0.1x 計價——真正的收益是 context 空間不是錢）。
- **`resolveInWorkDir` 的 TOCTOU 窗口**：解析與開檔之間 bash 可換 symlink。
  根治要 `openat2(RESOLVE_BENEATH)`（Linux）或逐段 `O_NOFOLLOW`。現況已擋掉「先種後用」。
- **authz 快取的 mtime 精度**：同一秒內兩次寫入且大小相同會漏判。升級路徑 fsnotify。
- **cron 的五個保留天花板**（base map 只增不減、執行中無法中斷…）：見 vault cron 筆記。
- **記憶檢索暴力 cosine**：數千節點內綽綽有餘，真巨量再上 ANN。
- **KG Stage 3b**（持久化／ANN 索引）：巨量才需，觸發未到。

---

## 🚫 對照 Hermes 的短板中，**刻意不追**的

寫下來，免得每次比較都重新焦慮一次。這些不是「還沒做」，是**判斷過不做**。

| 短板 | 為何不追 |
|---|---|
| **Kanban 共享工作面** | 先跑 orchestrator 一週；現在做是照抄別人的解法，解自己還沒有的問題 |
| **通道廣度**（WhatsApp／Signal／Email／iMessage） | positioning 已定調：廣度是它的護城河。`chatbot.Core` 平台無關，要加隨時是 transport adapter 的事，不是架構問題 |
| **Inter-agent messaging**（行程間路由） | 具名 agent 的持久記憶先解掉大部分需求；等 orchestrator 用出真需求再說 |
| **桌面 app／TUI** | 不同量級的產品面。C-Spec §七.4 已判定「按 cogito 進駐 IM 的定位，互動式介面是**選配非核心**」 |
| **面板的 Logs／Analytics 頁** | **不是缺口是分工**——trace 與成本已上 Langfuse，那邊本來就有多人認證 |

> 真正值得追的面板項只有 **remote auth（§4 tsnet）**；其餘「面板成熟度落後」多半屬上表。

---

## 🧭 具名 agent 的持久記憶 —— 讀半邊已完成（2026-07-24）

**讀半邊已實作**：`.claw/agents/<name>/memory/` per-agent 記憶，spawn 時注入子 agent role prompt
（`NewMemoryLoaderAt` / `LoadForInjection` + subagent.go；測試 subagent_memory_test.go）。記憶靠手寫填、
不與其他 agent 互見。**寫半邊（跑後反思→per-agent 提案→治理放行）仍延後**——淨新增子 agent 反思點
+ per-agent 治理面，等「orchestrator 實跑確認每次從零開始真的痛」再上。詳見 docs/multi-tenancy.md。

> ### ⚠️ 2026-08-05：這條的【題目】可能設錯了
>
> 研究 Hermes v0.20.0 的 Kanban 後（[task-board-research.md](task-board-research.md)）：
> 它對「同機多 agent 怎麼對齊」的答案**不是共享記憶，是共享一塊工作板**——不共享事實、
> 不共享上下文，**只共享任務狀態且只有 kernel 能寫**，衝突解決因此塌縮成「原子認領」。
>
> 對應到這條：痛點若真的出現，解法可能不是「讓 agent 互看記憶」，而是
> **「讓任務本身有持久狀態」**——agent 撿起一張卡，卡上已有 run 歷史與註解，
> 它不需要記得上次，因為**上下文在卡上，不在 agent 腦裡**。
>
> | | 舊題目 | 可能的新題目 |
> |---|---|---|
> | 要解的 | 具名 agent 跨次任務記憶不互見 | 跨次任務的**工作狀態**沒有落點 |
> | 做法 | per-agent 記憶寫半邊 + 治理放行 | 任務板：狀態機 + 持久 run 歷史 + per-task 認領 |
>
> **觸發條件不變**（orchestrator 實跑喊痛）。改的是「痛的時候要做什麼」，不是「現在就做」。
> 順帶標記兩個既有結構差異：cron 是**整輪一把 flock**（只允許 1 個跑者，非 N worker）、
> 子 agent 是同進程 goroutine（Hermes 刻意選行程隔離）。

<details><summary>原始分析</summary>

完整解是 inter-agent messaging（大，不做）。中間解：每個具名 agent 給自己的
`.claw/agents/<name>/memory/`，spawn 時載入、產出的記憶**走既有 governance 提案通道**。

Scout 下次記得上次查過什麼，而且記憶照樣要人放行——**與治理哲學相容，不是另開一套**。
規模中等（複用 `MemoryLoader`，~150–200 行）。**觸發條件**：orchestrator 實際跑過，
確認「專員每次從零開始」真的痛。

</details>

---

## 📌 面試後立刻做（零風險）——已清（07-24）

- [x] `/cron` 兩個 job 已開回（cron.json 每輪 Tick 重讀，改檔即生效）
- [x] `.env` 的 `COGITO_ALLOWED_USERS` 已確認是正常名單（無須 restore）
- [ ] `workspace/.sessions-archive/`：**`retrospect-e2e-41fcdf39.json` 別刪**——它是
      `delegate-and-verify-file` 提案來源的唯一 provenance 證據（agent 經 retrospect 技能
      ＋write_file 寫入、非 SkillSynthesizer 管線，故無 generated_by 戳記）；其餘 5 檔
      （accept／cron×3／subdepth-demo）確認不用可刪。提案本身已過 skillgate，晉升/丟棄待裁決。

---

## 🆕 對照 YC qm 的新增項（2026-08-04）

qm（YC 內部平台，2026-07-31 MIT 開源）盤點後有兩項進這份清單，細節在
[qm-learnings.md](qm-learnings.md)。
**先講清楚層級**：qm 自己不實作 agent loop——四個 runtime（Claude Agent SDK / Codex /
OpenCode / Pi）是 npm 依賴，`src/harness/` 全是轉接頭，45 個模組裡 harness 佔 1 個。
它在 cogito【上面】一層，不是同層對照組（那是 Hermes）。所以可移植的東西全在治理層：

- **🟢 SECURITY.md 彙整**（~1h、零風險）：內容已散在 README:36 與 incident-blacklist-bypass.md，
  缺的只是一頁能一次看完的「防什麼／**不防什麼**」。
- **🔴 記憶整併動作清單**：`MemorySynthesizer` 目前只 append，沒有任何路徑會 UPDATE/DELETE
  既有記錄——矛盾的記憶會並存在索引裡。補 `Consolidate()` 回 `UPDATE <n>`/`DELETE <n>`/`ADD`/`NONE`，
  產物走 P1 已做好的逐條審批；`DELETE` 走歸檔而非真刪。

另兩項（模型核准清單、egress 中間態）掛在「多租戶/外部委託真的發生」的觸發條件上，
未發生前做了也驗證不了——理由與更正過的前提見該文件。

---

## 建議順序（2026-08-05 更新；🟢 評測補完四項全數結案）

**📌 sessions-archive 裁決（5 分）→ 🟢 SECURITY.md（1h）→ 🔴 2. SWE-bench `-swe-env-setup` → 🔴 記憶整併動作清單**

理由：

1. **📌 sessions-archive**——掛了兩週的零風險小事，順手清掉（保留 `retrospect-e2e-*.json`
   當 provenance 證據，其餘 5 檔可刪；`delegate-and-verify-file` 提案的晉升/丟棄一併裁決）。
2. **🟢 SECURITY.md**——內容全部現成（README:36 + incident-blacklist-bypass.md），只是散著。
   對照 qm 後才發現：**我們比它誠實，卻沒有一頁能證明**。一小時補上唯一的敘事缺口。
3. **🔴 #2 SWE-bench `-swe-env-setup`**——現在最高價值，因為它**擋著另一個決策**。
   剛拿到 opus 4/5 的 baseline；若「提分的最大槓桿是備妥測試環境、不是換更強模型」成立，
   那個結論比多跑 15 題有價值，而且直接決定**要不要花 $4.9 把 SWE-bench 補到 n=20**
   ——如果 agent 是被環境卡住的，n=20 測的只是一個殘廢的 agent。成本：5 題重跑約 $1.2。
4. **🔴 記憶整併動作清單**——唯一真正改變能力的項目（矛盾記憶並存、recall 到哪條看運氣），
   但動 `.claw/memory/` 既有記錄、風險中等，排在三個零/低風險項之後。細節見
   [qm-learnings.md](qm-learnings.md) §1。

**其餘全部有觸發條件，不動**：tsnet 等部署形態、3b Telegram thread 等產品方向、
模型核准清單與 egress 等多租戶真的發生、任務板／layer-07 等 orchestrator 實跑喊痛、
SWE-bench n=20 等 #2 的結論。
