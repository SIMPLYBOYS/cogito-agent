#!/usr/bin/env bash
# 一鍵跑 SWE-bench Lite 子集：取資料 → cogito 生成 predictions → 官方 Docker 評測 → 解析 pass@1。
#
# 用法（在 repo 根目錄執行）：
#   scripts/run_swebench_lite.sh
#   N=20 MODEL=claude-opus-4-8 scripts/run_swebench_lite.sh      # 覆蓋題數/模型
#   OFFSET=150 N=5 scripts/run_swebench_lite.sh                  # 取某段（如中段的 requests）
#   PRUNE=1 scripts/run_swebench_lite.sh                         # 跑完清 docker 映像
#
# 需求：Docker daemon 在跑、python3、go、ANTHROPIC_API_KEY（生成階段）。x86_64。
# 產物與 harness 日誌都落在 $OUT（預設 .swebench/，已 gitignore）。
set -euo pipefail

N="${N:-10}"                        # 子集題數（先小後大：首跑建議 5~10）
MODEL="${MODEL:-claude-haiku-4-5}"  # 生成模型（省錢用 haiku；衝分換 opus，貴很多）
OFFSET="${OFFSET:-0}"               # 從第幾題起（資料按 instance_id 字母排序）
RUN_ID="${RUN_ID:-cogito-lite}"
WORKERS="${WORKERS:-4}"             # 官方 harness 並發容器數
OUT="${OUT:-.swebench}"
PRUNE="${PRUNE:-0}"
DATASET="princeton-nlp/SWE-bench_Lite"

LITE="$OUT/lite.jsonl"
PREDS="$OUT/preds.jsonl"

# --- 前置檢查（fail fast） ---
command -v python3 >/dev/null || { echo "❌ 需要 python3"; exit 1; }
command -v go >/dev/null || { echo "❌ 需要 go，且須在 repo 根目錄執行"; exit 1; }
docker ps >/dev/null 2>&1 || { echo "❌ Docker 未運行（docker ps 失敗）"; exit 1; }
[ -n "${ANTHROPIC_API_KEY:-}" ] || { echo "❌ 未設 ANTHROPIC_API_KEY（生成階段需要）"; exit 1; }

mkdir -p "$OUT"
echo "== 參數：N=$N MODEL=$MODEL OFFSET=$OFFSET RUN_ID=$RUN_ID WORKERS=$WORKERS =="

# --- ① 取官方 Lite 子集（免費） ---
echo "== ① 取子集 → $LITE =="
python3 scripts/fetch_swebench_lite.py -n "$N" --offset "$OFFSET" -o "$LITE"

# --- ② dry-run 檢視管線（免費、不呼叫 LLM） ---
echo "== ② dry-run 檢視前 3 題（不花錢） =="
go run ./cmd/bench -swebench "$LITE" -limit 3 -dry-run

# --- ③ cogito 生成 predictions（花錢：每題一次 agent loop） ---
echo "== ③ 生成 predictions（model=$MODEL）→ $PREDS =="
go run ./cmd/bench -swebench "$LITE" -limit "$N" -model "$MODEL" -predictions "$PREDS"

# --- ④ 官方 Docker 評測（免費但重；首次拉/建映像很慢）。在 $OUT 內跑，把 logs/報告都關進去。 ---
echo "== ④ 官方 harness 評測（Docker） =="
python3 -c "import swebench" 2>/dev/null || pip install swebench
( cd "$OUT" && python3 -m swebench.harness.run_evaluation \
    --dataset_name "$DATASET" \
    --predictions_path "$(basename "$PREDS")" \
    --max_workers "$WORKERS" \
    --run_id "$RUN_ID" )

# --- ⑤ 解析報告 → pass@1（分母用 submitted，非 dataset 全量 300） ---
echo "== ⑤ 結果 =="
( cd "$OUT" && python3 - "$RUN_ID" <<'PY'
import glob, json, os, sys
run_id = sys.argv[1]
cands = [f for f in glob.glob("*.json") if run_id in f] or glob.glob("*.json")
best = None
for f in cands:
    try:
        d = json.load(open(f))
    except Exception:
        continue
    if isinstance(d, dict) and "resolved_instances" in d:
        if best is None or os.path.getmtime(f) > os.path.getmtime(best[0]):
            best = (f, d)
if not best:
    print("（找不到報告 json，請看上面 harness 印出的 summary）")
    sys.exit(0)
f, d = best
submitted = d.get("submitted_instances") or d.get("completed_instances") or 0
resolved = d.get("resolved_instances", 0)
pct = resolved / submitted * 100 if submitted else 0
print(f"報告檔：{f}（dataset 全量 total={d.get('total_instances')}）")
print(f"→ 子集 resolved {resolved}/{submitted}  ==  pass@1 = {pct:.1f}%")
print(f'引用寫法："SWE-bench Lite {submitted} 題子集，pass@1 = {pct:.1f}%（cogito-agent）"')
PY
)

# --- ⑥ 選配：清 docker 映像（每題 ~1–2GB，累積很快） ---
if [ "$PRUNE" = "1" ]; then
  echo "== ⑥ docker system prune -a -f =="
  docker system prune -a -f || true
fi

echo "✅ 完成。產物在 $OUT/"
