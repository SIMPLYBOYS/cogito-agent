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

## 實跑基準（2026-07-22）

- 三專員並行、$0.33、約 90 秒。
- 各找到且只找到自己那面向；security 另外多抓一個 IDOR（真推理，非腳本）。
- 執行樹：3 個委派節點各可展開內部步數，逐步成本 $0.039 / $0.059 / $0.071。

## 誠實話術

「我**故意種了已知問題**讓 demo 確定性——機制是真的（三個專員獨立跑、隔離 context、
工具邊界由註冊表擋），標的是 fixture。」跟 SWE-bench「5 題我不宣稱什麼」同一個誠實動作。
