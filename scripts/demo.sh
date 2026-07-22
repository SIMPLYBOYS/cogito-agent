#!/usr/bin/env bash
# demo 前置：把 workspace 復位到 docs/demo-runbook.md 描述的已知狀態。
# 面試現場最不需要的就是「咦怎麼跟我昨天跑的不一樣」。
set -euo pipefail
cd "$(dirname "$0")/.."
CLAW=workspace/.claw

case "${1:-stage}" in
stage)
  # ②的刪除目標：可見、可驗收、丟了也不心疼。
  #
  # 【為何放 scratch/ 而不是 workspace 根目錄】cron 的 workDir 就是 workspace/，而 AGENTS.md
  # 有一條「不允許刪除根目錄的任何檔案」——目標放根目錄時 agent 會【停下來問人】而不是呼叫
  # rm -rf，於是沒有工具呼叫、也就沒有政策拒絕可演。實測踩過（2026-07-22 預演）。
  rm -rf workspace/scratch
  mkdir -p workspace/scratch/build
  printf 'demo artifact\n' > workspace/scratch/build/app.bin
  printf 'demo artifact\n' > workspace/scratch/build/app.map

  # 政策檔不能預先存在——②要演的是【內建正則 Ask → 無人值守降 Deny】，policy 會搶先 Deny。
  rm -f "$CLAW/policy.json"

  echo "已就緒："
  echo "  刪除目標  workspace/scratch/build/  ($(ls workspace/scratch/build | wc -l | tr -d ' ') 個檔)"
  echo "  政策檔    未建立（②的 Ask→Deny 靠內建正則；有 policy 會搶先 Deny、演不出降級）"
  echo "  提案技能  $(ls "$CLAW/skills-proposed" 2>/dev/null | wc -l | tr -d ' ') 個"
  echo "  生效技能  $(ls "$CLAW/skills" 2>/dev/null | wc -l | tr -d ' ') 個"
  ;;

pairing)
  # 第一幕前置：把自己踢出白名單，才演得出「未授權 → 配對 → 放行」。
  # 【備份 .env】那裡面是正式憑證，改壞了不是重跑一次就能救的。
  [ -f .env ] || { echo "找不到 .env" >&2; exit 1; }
  cp .env .env.demo-backup
  # 待審與既有授權都清掉，否則 demo 一開始就看到上次的殘留。
  rm -f "$CLAW/pairing-pending.json" "$CLAW/authorized-users.json"
  if grep -q '^COGITO_ALLOWED_USERS=' .env; then
    sed -i '' 's/^COGITO_ALLOWED_USERS=.*/COGITO_ALLOWED_USERS=nobody/' .env
  else
    printf 'COGITO_ALLOWED_USERS=nobody\n' >> .env
  fi
  echo "已就緒（原 .env 備份到 .env.demo-backup）："
  echo "  ALLOWED_USERS  nobody —— 你在 Slack 會是【未授權者】"
  echo "  待審／授權記錄  已清空"
  echo
  echo "演完務必還原：  ./scripts/demo.sh restore"
  ;;

restore)
  # 還原 .env。demo 結束後【一定】要跑——忘了的話 bot 重啟後沒有任何 bootstrap admin，
  # 也就沒有人能從 chat 批准任何人。
  [ -f .env.demo-backup ] || { echo "找不到 .env.demo-backup" >&2; exit 1; }
  mv .env.demo-backup .env
  echo "已還原 .env。"
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
  # demo 的第二幕要在面板【手動觸發】cron——排程器沿用 operator chat 的寫入閘，
  # 沒設 COGITO_DASH_CHAT=1 只能編輯不能執行。
  COGITO_DASH_CHAT=1 ./bin/claw-dashboard
  ;;

*)
  echo "用法: $0 {stage|pairing|restore|policy|serve}" >&2
  echo "  stage    第二幕前置：建刪除目標、清掉政策檔" >&2
  echo "  pairing  第一幕前置：備份 .env、把自己踢出白名單、清待審" >&2
  echo "  restore  還原 .env（demo 完【務必】跑）" >&2
  echo "  policy   第二幕結局三：寫入 deny 政策" >&2
  echo "  serve    起 dashboard" >&2
  exit 1
  ;;
esac
