# 面試 Demo Runbook（最終版 · 今天跟這份）

> **一句定位**：cogito-agent 是一個能在你的工作區裡真的動手做事的 Go LLM agent 框架——
> 多 agent 編排、逐步可回放且**每步有成本**的執行樹、動態授權、會進化但需把關的技能。
>
> **這是今天唯一要跟的 runbook。** 主秀＝多 agent code review（面板 chat，看 live 卡片）。
> 治理三幕的細節在 [`demo-runbook.md`](demo-runbook.md)（選用深度）；eval 數字在 [`eval-results.md`](eval-results.md)。

---

## 0. 一頁速查（面試時瞄這格就好）

```bash
# 出門前跑一次（三行）
./scripts/demo.sh magents    # 佈署標的/專員/技能到 workspace
./scripts/demo.sh serve      # 起面板（含 COGITO_DASH_CHAT=1）→ http://127.0.0.1:8091/chat
# ↑ 別用 `go run ./cmd/claw-dashboard`：不會隨改碼重編，會演到舊版
```

**主 query（面板 /chat 貼這句、送出）：**
> 用 orchestrate 技能審查 review-target/payment.go 能不能上線：派 correctness、security-auditor、performance 三個專員並行各審一個面向，整合成上線判斷。

**三句話術（背這三句）：**
1. 「三個窄專員**同一輪並行**、各自隔離 context，工具邊界由註冊表擋——連 `write_file` 都沒有。」
2. 「這**不是寫死的**：orchestrator 被 orchestrate 技能驅動，換個 query 一樣會拆、會並行派。」
3. 「執行樹**逐步可回放、每步有成本**——Hermes 的 watch-windows 關掉就沒了；我這棵是帳本。」

**出事按鈕**：跑不動 → 見 §7 B 版（零 LLM）。找不到檔 → query 一定要帶完整路徑 `review-target/payment.go`。

---

## 1. 出門前 / 開場前（pre-flight）

```bash
cd <repo>
./scripts/demo.sh magents        # 佈署 demo 資產（標的落 review-target/payment.go）
./scripts/demo.sh serve          # 面板 → http://127.0.0.1:8091/chat
```

**煙霧測試（幾秒、幾分錢，確認 serve 是新 binary、卡片會脈動）**——在 /chat 貼：
> 並行委派兩個子 agent：agent_type=correctness 執行 bash「echo A ok」；agent_type=performance 執行 bash「echo B ok」。各回報後總結。
跑完按「＋ 新對話」清掉。

**檢查清單**
- [ ] 面板用 `demo.sh serve` 起的（**不是** `go run`）——否則狀態不即時、無動畫。
- [ ] `workspace/review-target/payment.go` 在（`serve` 會自動補；仍可手動 `ls` 確認）。
- [ ] 網路 / provider key 正常（provider 缺 key 面板會退成唯讀、chat 停用）。
- [ ] 面板 /chat 按「＋ 新對話」開場，畫面乾淨。

---

## 2. 開場 hook（30 秒）— 一個不靠模型的數字（選用）

> 「先給你一個**不是 LLM 的**數字：我的檢索層。同一組多跳題，純關鍵字命中率 **0.50**，
> 接上知識圖譜變 **1.00**——因為多跳題的答案節點跟查詢字面**零重疊**，關鍵字設計上就撈不到。
> 而且我用一個測試釘住它：若有人把語料改簡單到關鍵字也能滿分，**測試會失敗**——不讓假勝利溜過。」

（要現場跑：`go run ./cmd/ingest -root internal/eval/testdata/mem_multihop -eval internal/eval/testdata/mem_multihop/labels.jsonl -k 3 -hops 1`。時間緊就直接口述數字，跳過。）

---

## 3. 主秀（約 90 秒）— 多 agent code review（面板 chat · live 卡片）

**先講問題**（10 秒）：
> 「review 一份要上線的金流程式,不該一個人從頭看到尾。我讓 orchestrator 把它**拆成三個面向、並行**派給三個窄專員,再整合成上線判斷。」

**現場跑**：在 /chat 貼主 query、送出。**當場會看到（不用 reload）**：

| 你會看到 | 你要說 |
|---|---|
| banner 掃光＋旋轉環 | 「進行中，這是即時串流。」 |
| orchestrator 先讀檔＋載入 orchestrate 技能（頂層，**不**進卡） | 「它先自己摸清檔案，再決定怎麼分工。」 |
| **三張兄弟卡同時「審查中⟳」**脈動 | 「三個專員**同一輪並行**上工，各自隔離 context。」 |
| 每張卡縮排各自的 read_file／grep 步驟 | 「事件按角色歸卡——correctness 看邏輯、security 追資料流、performance 盯迴圈。」 |
| 逐卡翻「✓ 報告完成」＋報告摘要 | 「各回各的面向,不重疊。」 |
| 回合 4 逐字冒出 GO/NO-GO 整合表 | 「orchestrator 把三份報告整合成一個明確結論。」 |

**收尾接執行樹**（點頂部 `/runs/operator`）：
> 「live 是過程；這棵**扇形執行樹**是帳本——每個委派節點可展開內部步數,**每步都有成本**
> （這次約 \$0.33、90 秒；三個委派各 \$0.039／\$0.059／\$0.071）。而且它**執行中會自動更新**,
> 不用 reload——連從 Telegram/Slack 發的任務也看得到。」

**標的裡種了什麼**（若被問「它真找得到嗎」）：payment.go 故意種了 4 個問題,一個對應一個面向——
硬編碼 live 金鑰＋SQL 注入（security）、`CanRefund` 用 `>` 應 `>=`（correctness）、`TotalForUser` N+1（performance）。
標的能編、`go vet` 乾淨,四個都要**讀碼**才找得到（linter 抓不到）——展示的是**推理**。

---

## 4. 話術：機制是真的、非寫死（最重要的一段）

被問「**是不是寫死的 UI／if agent=='correctness'**」時：

> 「不是。三件事證明它是真的：
> 1. orchestrator 是被 `orchestrate` **技能**驅動的——換個 query（審別的檔、甚至不是 review 的並行任務）一樣會拆、會並行派。
> 2. 三個專員各有自己的 `.claw/agents/<name>.md` 定義 role prompt、工具集、model；correctness/performance 還明寫 `model: opus, effort: high`。
> 3. 工具邊界是**註冊表 `Subset` 擋的**、不是 prompt 求它別寫——三個審查員 `tools: [read_file, bash]`,連 `write_file` 都沒有。」

**現場換 query 證明**（若對方想看）：貼
> 用 orchestrate 技能並行審查 lucky_draw.py：correctness 看邏輯、security-auditor 看安全、performance 看效能，整合成一份結論。

（`lucky_draw.py` 是 agent 稍早自己寫的真檔,非 fixture——換檔照樣拆。）

**誠實天花板**（主動講,別等被戳）：
> 「我**故意種了已知問題**讓 demo 確定——機制是真的,標的是 fixture。跟我對 SWE-bench『5 題我不宣稱什麼』同一個誠實動作。」

---

## 5. 深度彈藥（被問到 / 有時間才展開）

**為什麼窄專員、不用一個大 agent？**
> 窄專員各看一個面向、prompt 明令「不評論別人的職責」——報告不重疊,並行才乾淨。比廣的 `code-reviewer` 更適合並行。

**Plan Mode 沒開會不會有差？**
> Plan Mode 不是「讓模型會想」,是**狀態外部化**(PLAN.md/TODO.md)——解的是 context 壓縮丟失、跨重啟續跑、長程漂移。**短任務不開沒差**(這個 demo 就沒開),**長/會被打斷的任務才開**。換更強的模型不改變這條線——那是架構問題不是智商問題。

**為什麼是 opus 4.8？**
> 審邏輯/效能要細,correctness/performance 明寫 `model: opus, effort: high`;security-auditor 用預設。子 agent 可以各用各的模型,成本記進同一 session。

**vs Hermes Mission Control（5 個常駐 bot）？**
> 形狀不同：Hermes 是聊天室看到五個帳號常駐講話;cogito 的專員是**臨時、隔離 context** 的子 agent,
> multi-agent 呈現在**可回放、每步有成本的執行樹**裡——那是 watch-windows（並行 live 串流）做不到的。
> 我短板是廣度（Hermes 平台更成熟）,長處是**可稽核性**與**成本可見**。

**授權 / 安全？**（可接治理三幕,見 demo-runbook.md）
> 動態授權用 **Pairing**：未授權者自助產生配對碼、管理員一句 `pair approve` 放行,**零重啟、有稽核軌跡**。
> 誠實補一刀：無人值守降級那一幕,我演的是 policy Deny **擋住了**,但 agent 會**自我改寫繞道**——
> 我沒藏這個發現,把它變成 roadmap（Deny 應是目標終止,不是可重試的工具錯誤）。

**成本 / 評測數字**（見 eval-results.md）
> - 檢索：keyword 0.50 → +KG **1.00**（多跳題）。
> - 記憶 A/B ×5：通過率不變,但步數中位 8→3、成本 **−66%**。
> - 技能 A/B ×5：通過率 **1/5 → 4/5**,成本 +35%（載技能多一輪）;**Fisher p=0.206、n=5 未達顯著——我照實講**。
> - SWE-bench：官方 Docker harness 跑通,**\$0.24/題**;我不宣稱通過率。

---

## 6. Q&A 預判（問 → 一句答）

- **「多 agent 是平行還是假平行？」** → 真平行,同一輪吐多個 `spawn_subagent`,goroutine 併發跑,事件交錯進同一串流;我用 agent_type 把事件歸回各自的卡。
- **「子 agent 會不會亂改檔？」** → 審查員 registry 只給 `read_file/bash`,寫不了;要寫的（implementer）才給 write,且 worktree 隔離,並行寫不互撞。
- **「成本怎麼算的？」** → 自帶 tracker,快取讀 0.1x／寫 1.25x,每步落地 usage,執行樹逐步顯示,不依賴 Langfuse。
- **「面板有沒有認證？」** → 目前綁 loopback、CSRF 保護,能碰的人＝機器主人;下一步讓它讀反代注入的身分標頭,五行、不在應用層寫認證。
- **「跨行程怎麼看即時？」** → bot 每則訊息落盤、面板每次重讀磁碟,`/runs` 執行中輪詢換樹（~1.5s lag）;逐 token 串流只在同行程的 /chat。
- **「換小模型會壞嗎？」** → 機制不綁模型;demo 用 opus 求穩,eval 那幾張是 haiku 也能跑出差異。

---

## 7. 出事了怎麼辦（recovery）

- **面板演到舊版 / 沒動畫 / 狀態不即時** → 你在跑 `go run`。Ctrl-C,改 `./scripts/demo.sh serve`。
- **agent 說找不到 review-target** → query 一定帶**完整路徑** `review-target/payment.go`（別說「那個標的」逼它去搜）。`serve` 已會自動補檔。
- **query 太簡單沒觸發委派** → 明講「用 orchestrate 技能…派…三個專員並行」,別下「幫我看一下 payment.go」。
- **LLM／網路掛了（B 版 · 零 LLM · 完全可控）** → 直接開 `/runs/mademo2`（或任一既有多 agent session）,展示**同一棵執行樹**：一樣有三個委派節點、可展開內部步數、每步成本。話術：「這是一次真跑的回放,機制與剛才一致,只是不吃即時網路。」

---

## 8. 收尾（demo 後 cleanup）

```bash
./scripts/demo.sh restore        # 還原 policy / 授權名單等 demo 前置
```
- `/cron` 重新開啟先前停用的排程 job（若 demo 有動到）。
- `.env` 的 `COGITO_ALLOWED_USERS` 若驗收時設成 `nobody`,改回自己的 ID（只影響之後正常用 bot;Option A 走面板不吃這條）。
- 面板 /chat 按「＋ 新對話」清掉 demo 對話。

---

### 附：這場 demo 用到的東西在哪
- 標的與專員：`demo/mission-control/`（正本）→ `magents` 佈到 `workspace/.claw/agents/` 與 `workspace/review-target/`。
- 三個窄專員 role：`workspace/.claw/agents/{correctness,performance,security-auditor}.md`。
- 執行樹渲染：`internal/replay/`；面板：`cmd/claw-dashboard/`（`/chat` 逐 token SSE、`/runs` 執行中輪詢）。
- 治理三幕（選用深度）：`docs/demo-runbook.md`；評測數字：`docs/eval-results.md`；誠實事故：`docs/incident-blacklist-bypass.md`。
