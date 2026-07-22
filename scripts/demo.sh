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
  # ②要手動觸發的 job：冪等確保存在。沒有它，現場只能臨時到 /cron 建，很容易打錯路徑。
  python3 - <<'PYJOB'
import json, os
p = "workspace/.claw/cron.json"
jobs = json.load(open(p)) if os.path.exists(p) else []
PROMPT = "把 scratch/build 這個目錄整個刪掉，那是編譯產物"
job = next((j for j in jobs if j.get("prompt") == PROMPT), None)
if job is None:
    jobs.append({"id": "demo0000cron", "name": "清理建置產物", "schedule": "0 9 * * *",
                 "prompt": PROMPT, "enabled": True})
    print("  cron job  已建立（demo0000cron）")
else:
    # 清掉上次演練的執行紀錄，面板才不會顯示「上次 ok」誤導
    for k in ("last_run", "last_status", "last_error", "last_result"):
        job.pop(k, None)
    job["enabled"] = True
    print(f"  cron job  已就緒（{job['id'][:12]}，紀錄已清）")
json.dump(jobs, open(p, "w"), ensure_ascii=False, indent=2)
PYJOB
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

all)
  # 面試當天出門前的一鍵前置。順序有意義：stage 先復位靶機與 job，pairing 最後才動 .env
  # （它會把你踢出白名單，跑了就要記得 restore）。
  "$0" stage
  echo
  "$0" pairing
  echo
  echo "── 接著手動起兩個行程（各開一個終端）──"
  echo "  ./scripts/demo.sh serve      # 面板 → http://127.0.0.1:8091"
  echo "  go run ./cmd/claw            # bot（① 要收 Slack 訊息）"
  echo
  echo "⚠️  演完務必： ./scripts/demo.sh restore"
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

magents)
  # multi-agent demo 前置：把版控正本（demo/mission-control/）複製回 workspace/。
  # workspace/ 被 gitignore，故資產正本存在 demo/ 下，用這個指令佈署。
  D=demo/mission-control
  mkdir -p "$CLAW/agents" "$CLAW/skills/orchestrate"
  # agent 與技能是【共享資產】（從 workspace 根讀，跨頻道生效）——放一次即可。
  cp "$D/agents/correctness.md" "$D/agents/performance.md" "$CLAW/agents/"
  cp "$D/orchestrate-SKILL.md" "$CLAW/skills/orchestrate/SKILL.md"

  # 標的檔是【工作產物】，per-channel 隔離——面板 chat 在 workspace 根跑、Slack/TG 在
  # channels/<id>/ 子目錄跑。故同時佈到根（面板用）與每個既有頻道工作區（bot 用），
  # 這樣不管從哪個入口 demo 都找得到。
  deploy_target() {
    mkdir -p "$1/review-target"
    cp "$D/target/payment.go" "$D/target/go.mod" "$1/review-target/"
  }
  deploy_target workspace                          # 面板 operator chat
  chans=0
  for ch in workspace/channels/*/; do
    [ -d "$ch" ] || continue
    deploy_target "$ch"; chans=$((chans+1))
  done

  echo "已佈署 multi-agent demo 資產："
  echo "  標的      review-target/payment.go → workspace 根 + $chans 個頻道工作區"
  echo "  窄專員    correctness · performance（＋既有 security-auditor，共享）"
  echo "  編排技能  orchestrate（共享）"
  echo
  echo "  ⚠️ 若在【新頻道】demo：先在該頻道發一則訊息（建工作區），再重跑 magents。"
  echo
  echo "跑法（面板 chat 或 claw-cli）："
  echo '  用 orchestrate 技能審查 review-target/payment.go 能不能上線：'
  echo '  派 correctness、security-auditor、performance 三個專員並行各審一個面向，整合成上線判斷。'
  ;;

serve)
  go build -o bin/claw-dashboard ./cmd/claw-dashboard
  # 保險：operator chat 的 workDir＝workspace 根；標的不在就自動補一份（忘了先跑 magents、
  # 或 workspace 被清時，面板 demo 照樣找得到 review-target/payment.go）。
  if [ ! -f workspace/review-target/payment.go ]; then
    mkdir -p workspace/review-target
    cp demo/mission-control/target/payment.go demo/mission-control/target/go.mod workspace/review-target/
    echo "（已自動補上 review-target/payment.go 到 workspace 根）"
  fi
  echo "http://127.0.0.1:8091  （Ctrl-C 結束）"
  # demo 的第二幕要在面板【手動觸發】cron——排程器沿用 operator chat 的寫入閘，
  # 沒設 COGITO_DASH_CHAT=1 只能編輯不能執行。
  COGITO_DASH_CHAT=1 ./bin/claw-dashboard
  ;;

*)
  echo "用法: $0 {stage|pairing|restore|policy|serve}" >&2
  echo "  all      【一鍵】stage + pairing，並提示要起哪些行程" >&2
  echo "  stage    ②前置：建刪除目標、清政策檔、確保 cron job 存在" >&2
  echo "  pairing  第一幕前置：備份 .env、把自己踢出白名單、清待審" >&2
  echo "  restore  還原 .env（demo 完【務必】跑）" >&2
  echo "  policy   第二幕結局三：寫入 deny 政策" >&2
  echo "  magents  multi-agent demo 前置：佈署審查專員與標的檔" >&2
  echo "  serve    起 dashboard" >&2
  exit 1
  ;;
esac
