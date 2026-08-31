# Runbook：CI 失敗自動診斷（ci-autofix）

> Devin use-case「Auto-Fix Failing CI Builds」的**拉式**等價物：事件端在 GitHub Actions 發起
> （`workflow_run`），我們不需要 webhook 接收器。對照分析見 [devin-actions.md](devin-actions.md)。
> workflow 本體：[.github/workflows/ci-autofix.yml](../.github/workflows/ci-autofix.yml)。

## 它做什麼／刻意不做什麼

CI（四關：gofmt / vet / build / test -race）失敗時，自動開一個 `claw-cli` 任務：
抓失敗 log → 本地重現那個紅 → 排假設驗證 → 產出 `ci-diagnosis.md`
（失敗點｜根因｜最小修法含 diff 建議｜**排除掉的假設**）→ 進 job summary 與 artifact。

**刻意只診斷、不推碼**：

- 推碼會再觸發 CI——修不好就是「失敗→修→失敗」的**付費迴圈**
- 修不修由人決定，與本專案「提案→人審」的治理一致
- 不推碼＝不需要 `contents: write`，權限面最小

## 啟用（預設惰性，兩步）

1. repo **Settings → Secrets** 設 `ANTHROPIC_API_KEY`
2. repo **Settings → Variables** 設 `COGITO_AUTOFIX` = `1` ← 這是開關：每次觸發都花真錢

兩者缺一，job 直接跳過（不留紅燈雜訊——同 benchmark.yml 的 secret 檢查慣例）。
關掉：把 variable 改成 0 或刪除即可，不用動 workflow。

## 成本

- 模型寫死 `claude-haiku-4-5`（診斷夠用）；要換改 workflow 的 `CLAUDE_MODEL`
- 引擎內建**單次 Run 成本熔斷**（預設 $1，`workspace/.claw/config.json` 的 `max_cost_usd`）
  與回合上限（`max_turns`）——就算 prompt 失控也有硬防線
- job 級 `timeout-minutes: 20` 是最外層的斷路器

## 安全邊界

- 只在**本 repo** 的 CI 失敗後跑；fork PR 拿不到 secret，天然被排除
- `permissions` 只有 `contents: read` + `actions: read`（讀失敗 log 用）
- 診斷的是**失敗的那個 commit**（`workflow_run.head_sha`），不是 main 尖端——
  否則失敗與診斷可能對不上同一份碼

## 升級路徑（要用再做，不預先建）

| 想要 | 加什麼 |
|---|---|
| 診斷貼成 PR 留言 | `pull-requests: write` + 一步 `actions/github-script` 找 head_sha 對應的 PR |
| 真的自動開 fix PR | 另開分支 + `peter-evans/create-pull-request`；**務必**排除 `ci-autofix/*` 分支再觸發本 workflow，防迴圈 |
| 修完自動驗證 | prompt 撤掉「不要改碼」，改用 `claw-cli -verify "gofmt -l . && go vet ./... && go build ./... && go test ./..."`——goal 迴圈跑到綠或用盡次數 |

## 疑難排解

- **job 沒出現**：`workflow_run` 只認**預設分支上的** workflow 定義——merge 進 main 後才會生效
- **agent 沒產出 ci-diagnosis.md**：看「claw-cli 診斷」那步的輸出尾段；常見原因是熔斷先斷
  （log 太長、任務太開放）——縮 prompt 或調高 `max_cost_usd`
