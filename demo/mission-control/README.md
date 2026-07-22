# Mission Control demo — 多視角平行 code review（3 專員）

一支任務、一支專員小隊：orchestrator 收到「審這支檔能不能上線」，**同一輪並行**派出三個
【窄】專員各審一個面向，整合成上線判斷。首次看 demo 的人一眼就懂「review 當然要拆」。

**為何是這個形狀**（vs Hermes Mission Control 的 5 個常駐 bot）：cogito 的專員是**臨時、
隔離 context** 的子 agent，multi-agent 不是在聊天室看到五個帳號講話，而是在**執行樹**裡
看到一棵可回放、每步有成本的樹——那正是 Hermes 的 watch-windows（並行 live 串流）做不到的。

## 這些檔屬於版控（workspace/ 被 gitignore，故正本放這）

- `target/payment.go` `target/go.mod` — 標的，**故意種三個問題**，一個對應一個專員：
  - security：`apiToken` 寫死正式金鑰 + `LookupOrder` 的 SQL 字串拼接
  - correctness：`CanRefund` 用 `>` 應為 `>=`（恰好等額退不了）
  - performance：`TotalForUser` 迴圈裡逐筆查 DB（N+1）
  - 標的能編、`go vet` 乾淨——三個問題都要**讀碼**才找得到（linter 抓不到），展示的是推理。
- `agents/correctness.md` `agents/performance.md` — 兩個新的窄專員（security-auditor 複用既有）。
  三個都只有 `[read_file, bash]`——**連 write_file 都沒有**，是註冊表擋的、不是 prompt 求它別寫。
- `orchestrate-SKILL.md` — 編排技能（已補上三個窄專員的說明）。

`./scripts/demo.sh magents` 會把這些複製回 workspace/（見腳本）。

## 現場操作（面板 chat · 看 live 卡片）

**驅動點＝面板的 operator chat 框**，不是 Slack。理由：Slack 本來就有「正在輸入」的動態感，
面板才是這次補強的重點——委派當下三張專員卡【同時脈動】、內部步驟即時串流、逐卡翻「報告完成」，
全程零 reload。（Slack 驅動的任務不會串到 `/chat`；那條路只有 `/runs` 的靜態樹，要 reload 才更新。）

### 前置（出門前，一次）

```bash
./scripts/demo.sh magents    # 佈署標的/專員/技能到 workspace（標的落 review-target/payment.go）
./scripts/demo.sh serve      # 面板（已含 COGITO_DASH_CHAT=1）→ http://127.0.0.1:8091/chat
```

> ⚠️ 別用 `go run ./cmd/claw-dashboard`——它啟動時編一次、之後不隨改碼重編，會演到舊版
> （狀態不即時、沒動畫）。一律走 `demo.sh serve`（每次 `go build` 出新的）。

### 現場（約 90 秒）

1. 開 `http://127.0.0.1:8091/chat`，先按「＋ 新對話」清掉上下文（乾淨畫面）。
2. 貼這句、送出：
   > 用 orchestrate 技能審查 review-target/payment.go 能不能上線：派 correctness、security-auditor、performance 三個專員並行各審一個面向，整合成上線判斷。
3. 【當場看到】（不用 reload）：
   - banner 掃光＋旋轉環＝進行中；
   - orchestrator 先讀檔＋載入 orchestrate 技能（頂層事件，**不**進卡）；
   - **同一輪並行**吐出三張兄弟卡「🤖 correctness／security-auditor／performance 專員 · 審查中⟳」，**同時脈動**；
   - 每張卡縮排各自的 `read_file`／`grep` 步驟，做完翻「✓ 報告完成」＋一行報告摘要（各歸各卡，不混流）；
   - 最後 orchestrator 逐字冒出 GO/NO-GO 整合判斷。
4. 收尾點頂部 `/runs/operator`：秀【可回放】的扇形執行樹——每個委派節點可展開內部步數與逐步成本。
   一句話收束：「live 是過程，這棵樹是可稽核的帳本——Hermes 的 watch-windows 關掉就沒了。」

### 要講的一句（機制是真的、非寫死）

> 「這不是寫死的 UI，也沒有 `if agent=='correctness'` 的分支。orchestrator 是被 orchestrate
> 技能【驅動】的，換個 query 一樣會拆、會並行派。三個專員各自隔離 context、工具邊界由註冊表擋
> （連 `write_file` 都沒有）。我種的是【標的問題】讓結果確定，機制全是真的。」

## 實跑基準（2026-07-22 Slack；2026-07-23 面板 chat 複驗）

- 三專員並行、$0.33、約 90 秒。
- 各找到且只找到自己那面向；security 另外多抓一個 IDOR（真推理，非腳本）。
- 執行樹：3 個委派節點各可展開內部步數，逐步成本 $0.039 / $0.059 / $0.071。
- 面板 chat 複驗（2026-07-23）：三卡兄弟並列、同時脈動「審查中」，內部步驟【與】收尾報告
  各歸各卡（performance→N+1、security→SQL/憑證、correctness→CanRefund），主 agent 自身
  工具留頂層不入卡，回合 4 整合出四問題全抓的 GO/NO-GO。全程零 reload。

## 誠實話術

「我**故意種了已知問題**讓 demo 確定性——機制是真的（三個專員獨立跑、隔離 context、
工具邊界由註冊表擋），標的是 fixture。」跟 SWE-bench「5 題我不宣稱什麼」同一個誠實動作。
