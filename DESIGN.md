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
- **scoped**：無動態重規劃（plan 改寫）；Plan Mode 靠把計畫外部化到 `PLAN.md`/`TODO.md`（狀態外部化）而非內存。

### 2. 上下文工程
- **決定**：靜態系統層（[composer.go](internal/context/composer.go)）+ 滑動窗口（[session.go](internal/context/session.go)）+ **自校準壓縮器**（[compactor.go](internal/context/compactor.go)）。壓縮水位＝模型**真實上下文窗口 × 比例**，並用每次 API 回傳的真實 `PromptTokens` 反算 byte/token 比、EWMA 收斂——自動適配 Claude 200k / 本地 8k 等不同窗口。
- **對照**：Claude Code 的 auto-compact 走 **LLM 摘要**（語意壓縮、較貴）；cogito 走**機械折疊 + 自校準**（確定性、零額外 API，但不做語意壓縮）。這是刻意的取捨：可預測 + 省錢，犧牲壓縮率。
- **scoped**：不做 LLM 摘要式壓縮。

### 3. 長期記憶
- **決定**：對齊 CoALA 四型記憶。離散記錄（`.claw/memory/<slug>.md`）+ **索引常駐封頂 + `recall` 按需檢索**（[memory.go](internal/context/memory.go)，關鍵字評分、中文 bigram、零依賴）+ **LRU（檔案 mtime）+ 超量歸檔（可復原非刪除）**。寫入走 synth→提案→人工放行。
- **對照**：Hermes 有三層記憶 + 跨 session 用戶模型；cogito 的記憶是**檔案式、關鍵字檢索、無 embedding**（scoped），但**可審計、可手改、git 友善**，且遺忘用歸檔（接「失控控制」——記憶操作是新的失控面）。
- **scoped**：無向量/embedding 檢索、無 knowledge graph（介面已留，換 score/tokenize 即可升級）。

### 4. 工具系統與安全
- **決定**：可插拔註冊表 + 環繞式中間件（[registry.go](internal/tools/registry.go)）。安全是**多層**：HITL 危險指令審批（[approval.go](internal/slackbot/approval.go)，黑名單含 bash / write 路徑逃逸 / **遠端 MCP 工具**）推回 Slack 等 `approve`；OS 級 **Docker 沙箱**（[sandbox/](internal/sandbox/)，per-session 容器、只掛 workDir、`--network none`、限 mem/cpu/pid）是軟防線之外唯一擋得住逃逸的硬隔離。
- **對照**：Claude Code 用權限提示 + 工作目錄邊界；Codex 有沙箱與審批模式。cogito 把審批接到 **Slack HITL**（遠端可控）並提供**真正的 OS 級隔離**——這是本專案的招牌強項。
- **scoped**：沙箱是 Docker，未做更細的 seccomp/gVisor。

### 5. 多 agent 編排
- **決定**：`spawn_subagent`（agent-as-tool）——把深度探索委派給**只讀**子 agent，上下文隔離、可並行多路；可綁定技能正文只進子 context（主 context 不被汙染）。
- **對照**：Claude Code 的 Task 工具能派**會寫檔**的子 agent。cogito 子 agent **只讀**是刻意的安全選擇。
- **scoped（已知缺口）**：無並行**寫**子 agent / git worktree 隔離——並行修改會衝突，目前不支援。

### 6. 自我進化
- **決定**：四件套（[evolve/](internal/evolve/)）——技能自生成、AGENTS.md 記憶自更新、失敗 Reflexion、參數自調。**全部走安全閘**：只寫暫存提案（`skills-proposed/` / `AGENTS.proposed.md` / `config.proposed.json`）→ 確定性把關（結構 + 危險指令/憑證掃描）或人工 review → 才生效。技能對齊 [agentskills.io](https://agentskills.io) 開放標準。
- **對照**：Hermes 走到 **RL 微調（改模型權重）** 的飛輪；cogito 只在 **prompt/技能/記憶層**進化、**永不改權重**、**永遠 gated**。誠實說：進化較淺，但**安全、可審計、可回退**。
- **教訓（真實踩過）**：曾把一條過度擬合的爛慣例 `apply` 進 live AGENTS.md，污染了後續所有任務——閘只跟「人有沒有認真 review」一樣可靠。這強化了「提案而非自動生效」的設計。

### 7. 可觀測性與評測
- **決定**：OpenTelemetry → Jaeger/Langfuse（[observability/](internal/observability/)），LLM span 帶 `gen_ai.*` 語意約定 + `langfuse.observation.input/output`（看得到模型完整 I/O）；USD 成本追蹤（裝飾器）；**評測框架**（三段式 TestCase + RunSuite + Reflexion 重試 + 儀表板 + CI 門檻）+ **SWE-bench 相容管線**（[eval/swebench.go](internal/eval/swebench.go)）。
- **對照**：多數同級 side-project **零評測**。cogito 能對自己跑分、上 CI 門檻，並有 SWE-bench 相容的評測（方法論防作弊：測試在 agent 跑完才套）——這是「serious vs toy」的分水嶺。
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
| 長期記憶檢索 | ◐ 關鍵字+LRU（無 embedding） | ◐ | ◐ | ✅ 多層+RL |
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
