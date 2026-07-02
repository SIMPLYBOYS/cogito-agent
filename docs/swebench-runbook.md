# SWE-bench Lite Runbook（官方數字）

兩段式：**生成（cogito）→ 官方評測（Docker）**。生成的 patch 交給官方 harness，在官方映像內套用+跑測試，得出可引用的 `resolved%`（= pass@1）。

> 機器需求：**x86_64**（Intel/AMD）。官方映像是 x86_64，Apple Silicon 要 emulation、不建議。需 Docker daemon 運行；磁碟每實例 ~1–2GB（子集 10–20 題 ≈ 20–40GB）。

> **一鍵版**：`scripts/run_swebench_lite.sh` 把下面 0→3 串起來（含前置檢查、dry-run、解析 pass@1、選配 prune）。
> 例：`N=10 MODEL=claude-haiku-4-5 scripts/run_swebench_lite.sh`。想手動逐步跑就照下面各段。

## 0. 取得資料集
用內附腳本（stdlib，無需 pip install datasets）抓官方 `princeton-nlp/SWE-bench_Lite` 子集：
```bash
python3 scripts/fetch_swebench_lite.py -n 10 -o lite.jsonl
# 取某段（資料按 instance_id 字母排序，如 psf/requests 在中段）：
python3 scripts/fetch_swebench_lite.py -n 5 --offset 150 -o requests.jsonl
```
先不花錢檢視管線：`go run ./cmd/bench -swebench lite.jsonl -limit 3 -dry-run`。

## 1. 生成 predictions（cogito，需 ANTHROPIC_API_KEY）
```bash
go run ./cmd/bench \
  -swebench lite.jsonl -limit 10 \
  -model claude-haiku-4-5 \
  -predictions preds.jsonl
# → preds.jsonl，每行 {instance_id, model_name_or_path:"cogito-agent", model_patch:"<git diff>"}
```
- 生成階段只 clone@base_commit + 跑 agent 改原始碼 + 抓 `git diff`；**不套 test_patch、不自評**（agent 看不到驗證測試＝防作弊）。
- 成本：每實例一次 agent loop。haiku 子集 10 題約 $1–2；opus 高很多。先 haiku。

## 2. 官方評測（Docker，免費但重）
```bash
pip install swebench
python -m swebench.harness.run_evaluation \
  --dataset_name princeton-nlp/SWE-bench_Lite \
  --predictions_path preds.jsonl \
  --max_workers 4 \
  --run_id cogito-lite
# → 拉/建官方映像、套 model_patch + test_patch、跑 FAIL_TO_PASS/PASS_TO_PASS
# → 產出報告：resolved / total（= pass@1）
```

## 3. 報數字（誠實）
寫「**SWE-bench Lite N 題子集，pass@1 = X%（模型 M）**」——標明 N 與 M；不要假裝跑了全 300 題。子集數字一樣可引用。

## 注意
- **生成 vs 評測分離**是關鍵：可引用的數字必須來自**官方 harness + 官方映像 + 官方 resolved 指標**；cogito 只負責生成 patch。
- 生成輕量（不裝 repo 環境）→ agent 改 code 時不能跑該 repo 的測試自我驗證。若要更高分，可改成「在官方映像容器內生成」（agent 能跑測試），屬後續強化。
- 想先不花錢檢視管線：`go run ./cmd/bench -swebench lite.jsonl -limit 3 -dry-run` 印出每題的 Setup/Task/Validate 計畫。
