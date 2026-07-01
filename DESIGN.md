# 設計取捨（Design Decisions）

這份文件說明 cogito-agent 在 agent 設計各維度上**做了什麼決定、為什麼、以及刻意收斂（scoped）了什麼**。重點不是「功能多」，而是**判斷**——每個維度都標出取捨，並對照主流 agent（Claude Code、OpenAI Codex、Nous Hermes）的做法。競品定位見 [POSITIONING.md](POSITIONING.md)；逐輪 prompt 組裝的資料流見 [README 的架構圖](README.md#architecture)。

> 立場：單人 Go 專案**不與有產品團隊/實驗室的 agent 拼功能或廣度**——那是不同量級。本專案的價值在**設計深度、安全控制的連貫性、可觀測/評測的嚴謹，以及對自身取捨的完全自覺**。

---

## 貫穿全專案的設計原則

1. **框架強制 > 模型自覺**。失控控制（回合上限、成本熔斷、死迴圈指紋、沙箱）一律由框架層硬性執行，**不指望模型自己停手**。模型會壞掉；框架不該跟著壞。
2. **漸進式揭露無所不在**。技能、長期記憶、外部 MCP 工具都是「**索引常駐、正文按需**」——System Prompt 只放目錄，需要時才用工具取回。常駐上下文小，能力可無限長。
3. **能確定性就不放 LLM 進迴圈**。壓縮折疊、危險指令黑名單、參數調優建議都走**確定性規則**——更便宜、可審計、不額外燒 API、行為可預測。
4. **自我進化不繞過控制**。所有改寫未來行為的產物（技能/記憶/參數）一律「**只寫提案 → 人工或確定性閘把關 → 才生效**」，永不自動套用。
5. **重用同一抽象**。SWE-bench 實例＝既有三段式 TestCase；`recall` 鏡像 `read_skill`；MCP 危險審批重用 bash 黑名單。新東西盡量是舊抽象的一個實例。

---

## 逐維度的決定

### 1. Agent 控制流
- **決定**：ReAct（Thinking→Action→Observation）多輪迴圈（[engine/loop.go](internal/engine/loop.go)），但**三道硬防線由框架強制**：回合上限、per-task 成本熔斷、死迴圈指紋偵測（參數正規化看穿尾空格/路徑微差）。另加**成本軟著陸**——花費跨 80% 即提醒模型「停工具、立刻交付」，避免「錢花了、做好了、卻在交付前一刻被硬砍」。
- **對照**：Claude Code/Codex 也有 turn/權限上限；cogito 把「失控控制」當**第一主題**，且軟著陸這種「優雅降級」是多數 harness 沒做的。
- **scoped**：無動態重規劃（plan 改寫）；Plan Mode 靠把計畫外部化到 `PLAN.md`/`TODO.md`（狀態外部化）而非內存。其價值是**狀態durability、與模型 IQ 正交**：實測中斷一個 6 步任務後，重啟一個零對話記憶的新進程只說「繼續」，agent 仍能讀 `TODO.md` 從第 5 步續跑、零重工——再強的模型也贏不了「重啟後 context 歸零」，檔案化的計畫贏得了。預設關、`-plan` opt-in（短任務不需要）。
- **確定性步驟跳過**（[plan.go](internal/context/plan.go)）：斷點續跑「跳過已完成步驟」原本靠 LLM 自己重讀 `TODO.md` 猜位置（機率性、可能重做/漏做）。改由**框架每輪確定性解析 `TODO.md` 的 checkbox**，把「已完成幾步、下一個未完成步驟是哪一個」當**權威進度帳本**注入 system 訊息——由框架指定續跑點，而非模型猜。帳本準確性仍取決於模型誠實打勾（`- [x]`），但「讀哪裡、跳哪些」不再是模型的機率行為。
- **目標錨點注入 / 抗上下文漂移**（[plan.go](internal/context/plan.go) `ReadPlanGoal`）：多輪之後 agent 焦點會逐漸偏離原始目標（Context Drift，業界高頻坑）。故 Plan Mode 下框架**每輪自 `PLAN.md` 讀原始目標、固定釘進 system 訊息**當「目標錨」——目標不靠滾動摘要的 salience「盡量保留」（軟），而是每輪從檔案硬注入（不會被摘要截斷），行動偏離即拉回。與進度帳本同機制、同來源（外部化檔案），零額外 LLM。
- **狀態管理三模式的取捨（刻意只選 Checkpoint-Resume）**：對照業界三種 agent 狀態管理——① Checkpoint-Resume（快照+續跑）② Event Sourcing（全量事件日誌+重放）③ FSM（顯式狀態機）。cogito **刻意只落在 ①**：SessionSnapshot 落盤 + Plan Mode 帳本 + 暫時性中斷自動續跑（[chatbot/core.go](internal/chatbot/core.go)）。**不做 ②**——其稽核/回放/時間旅行價值我們已由 **OTel 全鏈追蹤 + append 導向的 history** 拿到 ~80%，重建成事件日誌是過度設計。**不做 ③**——顯式 FSM 會扼殺 ReAct 的彈性，且「異常可攔截/行為可控」已由護欄（回合/成本熔斷、HITL 審批、RecoveryManager）達成。呼應「沒有銀彈、按任務選模式」：這是**有意識的不做**，非缺漏。
- **錯誤恢復是分層的，且各層的「成長」放對地方**（[recovery.go](internal/context/recovery.go)）：
  1. **RecoveryManager = 有界 first-aid**——規則式，只收「模型沒提示就會做錯」的少數高價值 nudge（如 edit_file stale old_text→先 read_file）。**刻意不追求覆蓋率、不該一直加 rule**（會變打地鼠）；主模型本就讀得懂多數錯誤。
  2. **長尾**交給模型自己（error-as-observation 回灌）。
  3. **恢復能力隨時間提升**＝**自我進化層**：失敗→Reflexion 萃取「遇到 X 做 Y」→記憶/KG（gated）→下次 recall 自動帶出。這才是會學習、零手動的 scalable 版本。
  > 設計判斷：當「提升恢復」的直覺是「加 rule」時，那是 smell——成長該發生在學習層，不是硬編碼層。

### 2. 上下文工程
- **決定**：靜態系統層（[composer.go](internal/context/composer.go)）+ 滑動窗口（[session.go](internal/context/session.go)）+ **自校準壓縮器**（[compactor.go](internal/context/compactor.go)）。壓縮水位＝模型**真實上下文窗口 × 比例**，並用每次 API 回傳的真實 `PromptTokens` 反算 byte/token 比、EWMA 收斂——自動適配 Claude 200k / 本地 8k 等不同窗口。
- **對照**：Claude Code 的 auto-compact 走 **LLM 摘要**（語意壓縮、較貴）；cogito 的**機械折疊 + 自校準**是 always-on 的 **OOM 硬防線**（確定性、零額外 API）。
- **滾動摘要 + history 有界化（對話式入口預設開）**：純滑動窗口有個真實產品缺陷——舊於窗口的訊息從上下文消失，長對話會失憶。故加**第二層**（[engine/summarizer.go](internal/engine/summarizer.go)）：history 超過門檻時，把超出逐字尾的舊訊息 **LLM 增量摺進滾動摘要**（salience 內建於 prompt：保留決策/約束/使用者更正/未解事項，丟例行成功/寒暄），並從 `session.history` **真正逐出**（重建 slice 讓 GC 回收）。於是 history 恆為 `[摘要] + 末 N 逐字`——**跨逐出連貫性與記憶體同時收斂**。只在超門檻時付一次 LLM（多數短對話零成本）；失敗保留原歷史下輪再試（不丟資料）。摘要併進 system 訊息不破壞 user/assistant 交替。
- **兩層分工**：機械 Compactor 防「單輪窗口內爆量」（確定性、無 API）；滾動摘要防「跨逐出失憶 + RAM 無界」（語意、opt-in）。`COGITO_SUMMARY=off` 可關；bench/一次性任務預設關（`NewAgentEngine` 預設 false）保跑分確定性。
- **scoped**：摘要觸發門檻/逐字尾長度目前寫死（tail 20 / trigger 40）；未做 per-session 動態調參。

### 3. 三層記憶（工作 / 會話 / 長期）
記憶是**三層遞進**（窗口 ⊂ 全歷史 → 蒸餾記錄），各有獨立實作、明確壽命與持久化策略，不是同一 buffer 換名字：

```
① 工作記憶 Working  這一輪送進 LLM 的窗口
   GetWorkingMemory(20) 末 N 則滑動窗 + Compactor 字元級折疊  session.go:95 / loop.go:157
   壽命：單輪，每 turn 重取重折
② 會話記憶 Session  這條對話（滾動摘要 + 末 N 逐字，有界）
   Session.history（RWMutex）+ GlobalSessionMgr 按 convID 分池      session.go:14,139
   長對話：舊訊息 LLM 摺進 session.summary 並逐出 → history 有界    engine/summarizer.go
   可持久化：SessionStore write-through 落盤（COGITO_SESSION_DIR）  session_store.go
   壽命：整段對話；設環境變數後跨重啟續傳（預設純記憶體）
③ 長期記憶 Long-term  跨會話/重啟/平台的蒸餾知識
   離散記錄 .claw/memory/*.md + 索引常駐 + recall 按需 + KG 關聯    memory.go / graph.go
   壽命：永久
```
①②的上下文工程細節見上節 2；以下深入第③層長期記憶。

- **決定**：對齊 CoALA 四型記憶。離散記錄（`.claw/memory/<slug>.md`）+ **索引常駐封頂 + `recall` 按需檢索**（[memory.go](internal/context/memory.go)，關鍵字評分、中文 bigram、零依賴）+ **LRU（檔案 mtime）+ 超量歸檔（可復原非刪除）**。寫入走 synth→提案→人工放行。
- **對照**：Hermes 有三層記憶 + 跨 session 用戶模型；cogito 的記憶是**檔案式、關鍵字檢索、無 embedding**（scoped），但**可審計、可手改、git 友善**，且遺忘用歸檔（接「失控控制」——記憶操作是新的失控面）。
- **抗幻覺記憶（Hallucinated Memory）**：兩道防線——① **來源標註（provenance）**：放行的記錄自帶 `recorded:` 時間戳 + body 的「由誰/何時/從哪個任務沉澱」（[memory_synth.go](internal/evolve/memory_synth.go)），recall 時渲染給模型看，讓「真實檢索到的記憶」可溯源、與模型自產內容區分；② **強制不確定性聲明**：recall 查無時明確回「我沒有相關的長期記憶、切勿杜撰來源/時間/內容」（[recall.go](internal/tools/recall.go)），而非回空讓模型腦補。另加結構性防線：recall 是 tool_result（**檢索**）與模型**生成**天然分離；記憶即磁碟檔本身（無「索引 vs 原文」分岔，故 hash 交叉驗證 N/A）。
- **KG（已做 Stage 1）**：記錄=節點、`[[links]]`=邊、`tags`=label；`recall` 回的是**連通子圖**（種子 + k 跳鄰域 + 它們之間的明確關係，[graph.go](internal/context/graph.go)），讓模型做 RAG 做不到的多跳關係推理。
- **多文件 ingest（Stage 2a 已做）**：`cmd/ingest` 把 md 目錄結構式 ingest 成節點 + `edges.jsonl`（[ingest.go](internal/context/ingest.go)，確定性、不花錢），ingested 文件即進同一張圖供 recall 跨檔多跳。
- **typed 關係抽取（Stage 2b 已做）**：LLM 從節點文字抽 typed 關係（depends-on/part-of…，[evolve/kg_extract.go](internal/evolve/kg_extract.go)）→ 提案 → **gate**（信心門檻/幻覺端點丟棄/去重/每節點封頂，[kg_gate.go](internal/context/kg_gate.go)）→ 併入 `edges.jsonl`。完全走 propose→gate→apply，與自我進化同一安全鐵律——這是 KG 勝 RAG 的來源，且不繞過控制。
- **混合選種子（Stage 3a 已做，opt-in）**：設了 `COGITO_EMBED_MODEL`（OpenAI 相容 `/embeddings`，雲端或本地）時，`recall` 用 embedding 語意選種子（[embed.go](internal/context/embed.go)，暴力 cosine + sidecar 向量檔），未配置則零依賴退回關鍵字。Anthropic 無 embeddings 端點，故語意檢索一律經 OpenAI 相容端點——與多 Provider DNA 一致。
- **scoped**：持久化 / ANN 索引（Stage 3b）待巨量才做。分階段設計見 [docs/kg-spec.md](docs/kg-spec.md)。

### 4. 工具系統與安全
- **決定**：可插拔註冊表 + 環繞式中間件（[registry.go](internal/tools/registry.go)）。安全是**多層**：HITL 危險指令審批（[approval.go](internal/slackbot/approval.go)，黑名單含 bash / write 路徑逃逸 / **遠端 MCP 工具**）推回 Slack 等 `approve`；OS 級 **Docker 沙箱**（[sandbox/](internal/sandbox/)，per-session 容器、只掛 workDir、`--network none`、限 mem/cpu/pid）是軟防線之外唯一擋得住逃逸的硬隔離。
- **對照**：Claude Code 用權限提示 + 工作目錄邊界；Codex 有沙箱與審批模式。cogito 把審批接到 **Slack HITL**（遠端可控）並提供**真正的 OS 級隔離**——這是本專案的招牌強項。
- **scoped**：沙箱是 Docker，未做更細的 seccomp/gVisor。

### 5. 多 agent 編排
- **決定**：`spawn_subagent`（agent-as-tool）——把深度探索委派給**只讀**子 agent，上下文隔離、可並行多路；可綁定技能正文只進子 context（主 context 不被汙染）。
- **對照**：Claude Code 的 Task 工具能派**會寫檔**的子 agent。cogito 子 agent **只讀**是刻意的安全選擇。
- **scoped（已知缺口）**：無並行**寫**子 agent / git worktree 隔離——並行修改會衝突，目前不支援。

### 6. 自我進化
- **決定**：四件套（[evolve/](internal/evolve/)）——技能自生成、AGENTS.md 記憶自更新、失敗 Reflexion、參數自調。**全部走安全閘**：只寫暫存提案（`skills-proposed/` / `AGENTS.proposed.md` / `config.proposed.json` / `kg/edges.proposed.jsonl`）→ 確定性把關（結構 + 危險指令/憑證掃描）或人工 review → 才生效。技能對齊 [agentskills.io](https://agentskills.io) 開放標準。
- **兩種觸發（harness 兜底 + agent 自主）**：任務結束時 **post-task hook 由 harness 保證觸發**，反思出技能/記憶/KG 關係（可靠，不指望 agent 記得）；同時暴露 `consolidate` 工具讓 **agent 判斷有可複用價值時自己提前沉澱**（自主）。兩者產物同樣 gated。這就是「框架強制觸發 + 模型判斷內容」落在自我進化上的體現。
- **對照**：Hermes 走到 **RL 微調（改模型權重）** 的飛輪；cogito 只在 **prompt/技能/記憶層**進化、**永不改權重**、**永遠 gated**。誠實說：進化較淺，但**安全、可審計、可回退**。
- **教訓（真實踩過）**：曾把一條過度擬合的爛慣例 `apply` 進 live AGENTS.md，污染了後續所有任務——閘只跟「人有沒有認真 review」一樣可靠。這強化了「提案而非自動生效」的設計。

### 7. 可觀測性與評測
- **決定**：OpenTelemetry → Jaeger/Langfuse（[observability/](internal/observability/)），LLM span 帶 `gen_ai.*` 語意約定 + `langfuse.observation.input/output`（看得到模型完整 I/O）；USD 成本追蹤（裝飾器）；**評測框架**（三段式 TestCase + RunSuite + Reflexion 重試 + 儀表板 + CI 門檻）+ **SWE-bench 相容管線**（[eval/swebench.go](internal/eval/swebench.go)）。
- **對照**：多數同級 side-project **零評測**。cogito 能對自己跑分、上 CI 門檻，並有 SWE-bench 相容的評測（方法論防作弊：測試在 agent 跑完才套）——這是「serious vs toy」的分水嶺。
- **記憶也有評測**：Level 1 檢索評測（[eval/memeval.go](internal/eval/memeval.go)，`cmd/ingest -eval <labels>`）對標註集算 hit@k / MRR，**三模式對照**（關鍵字 / embedding / 關鍵字+KG）；Level 2 任務影響 A/B（[eval/memab.go](internal/eval/memab.go)，`cmd/bench -mem-ab`）同一任務在「無/有相關記憶」下各跑一次比回合/成本。**實測**：相關記憶讓同一任務從 8 回合→3、成本降 ~66%——記憶的價值有數字、不靠感覺。
- **狀態**：SWE-bench 管線已端到端真跑驗證（真 agent 修真 bug、隱藏測試驗證通過）；官方 Lite 的 300 題數字需官方 Docker 環境，屬執行 infra。

### 8. Provider 抽象
- **決定**：`LLMProvider` 介面（[provider/](internal/provider/)）+ `FromEnv`。預設 Claude（官方 SDK）；`COGITO_PROVIDER=openai` 走**手寫**的 OpenAI 相容 client（不加重依賴），`OPENAI_BASE_URL` 可指 vLLM/Ollama/OpenRouter/Groq——一招拿到廣度。OTLP 認證 header 由 `LANGFUSE_*` 金鑰自動派生（單一真相源，不手編 base64 而漂移）。
- **scoped**：bench 跑分仍 Claude-only；Gemini 經 OpenAI 相容間接支援、無原生。

### 9. 部署形態
- **決定**：單一 Go binary、近零依賴、**手寫** MCP/JSON-RPC client（stdio + Streamable HTTP，無第三方 SDK）。self-hosted、Anthropic 優先。
- **對照**：Hermes 是 Python 框架、Claude Code/Codex 是產品。cogito 的利基：**可審計、可自託管、單檔部署**。

---

## 對照表

| 維度 | cogito-agent | Claude Code | Codex | Hermes |
|---|---|---|---|---|
| 控制流硬防線 | ✅ 回合/成本/死迴圈/軟著陸 | ◐ 限制 | ◐ 限制 | ◐ |
| 上下文壓縮 | ✅ 機械+自校準 | ✅ LLM 摘要 | ✅ | ✅ |
| 長期記憶檢索 | ◐ 圖子圖+混合(關鍵字/embedding)+LRU | ◐ | ◐ | ✅ 多層+RL |
| HITL + OS 沙箱 | ✅✅ Slack 審批 + Docker 隔離 | ◐ 權限提示 | ✅ 沙箱 | ◐ |
| 多 agent | ◐ 只讀子 agent | ✅ 可寫子 agent | ◐ | ✅ |
| 自我進化 | ◐ prompt/技能/記憶層 + gated | ✗ | ✗ | ✅✅ RL 飛輪 |
| 可觀測 + 評測 | ✅✅ OTel + 跑分 + CI + SWE-bench | ◐ | ◐ | ◐ |
| 模型/平台廣度 | ◐ 多 Provider / Slack | ✅ 產品級 | ✅ | ✅✅ 200+/多平台 |
| 部署形態 | ✅ 單 Go binary 自託管 | 產品 | 產品 | Python 框架 |

（✅✅=招牌強項，✅=做得好，◐=有但收斂，✗=未做。對其他 agent 僅就公開可知的設計特性概述。）

---

## 刻意不做的（YAGNI / scoped，及理由）

- **RL 微調飛輪**：要資料/算力/模型血統，非單人專案可達；且改權重與「可審計、可回退」的安全主題相悖。
- **向量/embedding 記憶、knowledge graph**：現階段關鍵字檢索夠用；`recall` 介面已留好升級點。
- **並行寫子 agent / worktree**：併發寫衝突複雜，需求未到。
- **多即時通訊平台**：先把 Slack 一條做深，廣度是之後的事。
- **200+ 模型原生**：OpenAI 相容端點已間接覆蓋大半，不為廣度而廣度。

**敢標 scoped、並講清楚 why，本身就是設計判斷**——比假裝全能可信。

---

相關：[POSITIONING.md](POSITIONING.md)（競品定位）、[README.md](README.md)（功能與架構）。
