#!/usr/bin/env bash
# demo 前置：把 workspace 復位到 docs/demo-runbook.md 描述的已知狀態。
# 面試現場最不需要的就是「咦怎麼跟我昨天跑的不一樣」。
set -euo pipefail
cd "$(dirname "$0")/.."
CLAW=workspace/.claw

case "${1:-stage}" in
stage)
  # 結局一/二的刪除目標：可見、可驗收、丟了也不心疼。
  rm -rf workspace/build
  mkdir -p workspace/build
  printf 'demo artifact\n' > workspace/build/app.bin
  printf 'demo artifact\n' > workspace/build/app.map

  # 政策檔要「現場才寫」——結局三的戲劇性全靠它一開始不存在。
  rm -f "$CLAW/policy.json"

  echo "已就緒："
  echo "  刪除目標  workspace/build/  ($(ls workspace/build | wc -l | tr -d ' ') 個檔)"
  echo "  政策檔    未建立（結局三現場寫）"
  echo "  提案技能  $(ls "$CLAW/skills-proposed" 2>/dev/null | wc -l | tr -d ' ') 個"
  echo "  生效技能  $(ls "$CLAW/skills" 2>/dev/null | wc -l | tr -d ' ') 個"
  ;;

policy)
  # 結局三：現場貼這段比手打快，但講解時仍要逐行念。
  cat > "$CLAW/policy.json" <<'JSON'
{
  "rules": [
    { "tool": "bash", "match": "rm -rf", "action": "deny",
      "reason": "遞迴刪除一律走人工，不接受 agent 自行判斷" }
  ]
}
JSON
  echo "已寫入 $CLAW/policy.json —— 重跑同一句話，這次連問都不會問。"
  ;;

serve)
  go build -o bin/claw-dashboard ./cmd/claw-dashboard
  echo "http://127.0.0.1:8091  （Ctrl-C 結束）"
  ./bin/claw-dashboard
  ;;

*)
  echo "用法: $0 {stage|policy|serve}" >&2
  exit 1
  ;;
esac
