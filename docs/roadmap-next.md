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

### 3b. Telegram 論壇主題（forum topics）路由 — **新發現的缺口**

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

### 5. 面板讀反向代理注入的身分標頭（tsnet 的輕量替代）

`authzpage.go` 的 `operatorID` 現在是 const `"dashboard(operator)"`，只用在兩處。
改成讀 `X-Forwarded-User`（約 5 行），稽核就從「從面板做的」升級成「aaron@example.com 做的」。

**前提**：面板只綁 loopback 且**唯一入口是可信代理**，否則標頭可偽造。
**與 tsnet 的取捨**：這個輕、但只在有反代時成立；tsnet 重、但信任鏈更硬。

---

## 🟢 評測補完（便宜，但要時間跑）

| 項目 | 現況 | 該做到 |
|---|---|---|
| **skill-ab 樣本數** | 1/5→4/5，**Fisher p=0.206 未達顯著**、有一次反例 | n ≥ 20 才能下結論 |
| **A/B 樣本數門檻** | 跑一次就輸出結論 | 對齊既有 `evolve.MinVerifySamples = 10` |
| **SWE-bench opus 分數** | 生成完成（$1.21），**評測中途叫停 → 無分數** | 跑完官方 harness |
| **embedder 配置** | `embedding` 模式 N=0 被跳過 | 三模式對照名副其實 |

> ⚠️ 前兩項是**已對外寫進 README 的數字**的支撐——優先度高於看起來更酷的紅色項目。

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

## 🧭 具名 agent 的持久記憶（短板「專員無記憶」的低成本半解）

完整解是 inter-agent messaging（大，不做）。中間解：每個具名 agent 給自己的
`.claw/agents/<name>/memory/`，spawn 時載入、產出的記憶**走既有 governance 提案通道**。

Scout 下次記得上次查過什麼，而且記憶照樣要人放行——**與治理哲學相容，不是另開一套**。
規模中等（複用 `MemoryLoader`，~150–200 行）。**觸發條件**：orchestrator 實際跑過，
確認「專員每次從零開始」真的痛。

---

## 📌 面試後立刻做（零風險）——已清（07-24）

- [x] `/cron` 兩個 job 已開回（cron.json 每輪 Tick 重讀，改檔即生效）
- [x] `.env` 的 `COGITO_ALLOWED_USERS` 已確認是正常名單（無須 restore）
- [ ] `workspace/.sessions-archive/`：**`retrospect-e2e-41fcdf39.json` 別刪**——它是
      `delegate-and-verify-file` 提案來源的唯一 provenance 證據（agent 經 retrospect 技能
      ＋write_file 寫入、非 SkillSynthesizer 管線，故無 generated_by 戳記）；其餘 5 檔
      （accept／cron×3／subdepth-demo）確認不用可刪。提案本身已過 skillgate，晉升/丟棄待裁決。

---

## 建議順序（07-24 更新；原順序的 get 與 caching 已結案）

**🟢 評測補完 → 🔴 2. SWE-bench `-swe-env-setup` → 🟡 3b. Telegram thread（若走 orchestrator 路線）→ 🟡 4/5. 面板身分（tsnet 或反代標頭）**

理由：綠的那組能把「p=0.206」升級成真結論，而那是**已經寫進 README 的數字**，僅剩的阻礙
是一筆小成本（~$0.7）；#2 是「一個結論值 25 題」的驗證；3b 取決於產品方向（orchestrator
常駐與否）；4/5 等部署形態明朗（上雲/反代）再選邊。
