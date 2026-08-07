#!/usr/bin/env python3
"""量測「委派時要重新交代多少」——任務板（docs/task-board-research.md）的觸發線量測。

跑法：python3 scripts/subagent_briefing_cost.py [sessions 目錄...]
      預設掃 workspace/.sessions 與 workspace/.sessions-archive。

為什麼需要這支：roadmap 上「具名 agent 持久記憶／任務板」那條的觸發條件原本寫
「等 orchestrator 實跑喊痛」——但沒寫「怎麼知道痛了」。沒有量測計畫的觸發條件，
實質上就是無限期延後包裝成紀律。這支把它變成數字。

看什麼：子 agent 是隔離 context，orchestrator 每次委派都得把脈絡重講一次寫進
task_prompt。那些字元就是「每次從零開始」的直接成本。

判讀（2026-08-05 首次量測：30 次委派 / 9 session，累計 ~13.5k tokens ≈ $0.07@opus）：
結論是【還不痛】。真正貴的是少數把整份原始碼貼進 task_prompt 的案例——那不是
「記憶不互見」，是「忘了子 agent 共用同一個工作區」，已在 orchestrate 技能補上提醒。
"""
import glob
import json
import os
import sys
from collections import Counter

# 觸發線：任一項達標就值得重新考慮任務板（理由見 docs/task-board-research.md）
THRESHOLD_TOTAL_TOKENS = 50_000  # 累計重新交代量
THRESHOLD_REPEAT_TYPE = 5        # 單一 session 內跨次任務重複派同型 agent 的次數

CHARS_PER_TOKEN = 2  # 中文粗估；只用來給數量級，不當精算


def collect(dirs):
    rows = []
    for d in dirs:
        for f in glob.glob(os.path.join(d, "*.json")):
            try:
                data = json.load(open(f, encoding="utf-8"))
            except Exception:
                continue
            spawns = []
            for m in data.get("history", []):
                for tc in m.get("tool_calls") or []:
                    if tc.get("name") != "spawn_subagent":
                        continue
                    args = tc.get("arguments")
                    if isinstance(args, str):  # 舊格式可能是字串
                        try:
                            args = json.loads(args)
                        except Exception:
                            args = {}
                    spawns.append(args or {})
            if spawns:
                rows.append((data.get("id") or os.path.basename(f), spawns))
    return sorted(rows, key=lambda r: -len(r[1]))


def main() -> None:
    dirs = sys.argv[1:] or ["workspace/.sessions", "workspace/.sessions-archive"]
    rows = collect(dirs)
    if not rows:
        print("沒有含 spawn_subagent 的 session——尚無資料可判斷。")
        return

    briefs, types, worst_repeat = [], Counter(), 0
    print(f"{'session':30} {'次':>3}  簡報字元數")
    for sid, spawns in rows:
        lens = [len(s.get("task_prompt", "")) for s in spawns]
        kinds = Counter(s.get("agent_type") or "(預設)" for s in spawns)
        worst_repeat = max(worst_repeat, sum(c - 1 for c in kinds.values() if c > 1))
        briefs += lens
        types.update(kinds)
        print(f"{sid[:28]:30} {len(spawns):>3}  {lens}")

    briefs.sort()
    total_tok = sum(briefs) // CHARS_PER_TOKEN
    big = [b for b in briefs if b > 2000]
    print(f"\n=== 彙總（{len(briefs)} 次委派 / {len(rows)} 個 session）===")
    print(f"每次重新交代：中位數 {briefs[len(briefs) // 2]} 字元、最長 {max(briefs)}、最短 {min(briefs)}")
    print(f"累計 {sum(briefs)} 字元 ≈ {total_tok} tokens"
          f"（haiku ${total_tok / 1e6:.4f} / opus ${total_tok * 5 / 1e6:.4f}）")
    print(f"簡報 >2000 字元（多半是把整份檔案貼進去）：{len(big)}/{len(briefs)} 次")
    print(f"agent_type 分布：{dict(types)}")

    print("\n=== 觸發線 ===")
    hit = False
    for label, got, want in [
        ("累計重新交代 tokens", total_tok, THRESHOLD_TOTAL_TOKENS),
        ("單 session 重複派同型", worst_repeat, THRESHOLD_REPEAT_TYPE),
    ]:
        ok = got >= want
        hit = hit or ok
        print(f"  {'🔴' if ok else '🟢'} {label}：{got} / 門檻 {want}")
    print("\n→ " + ("已達觸發線，值得重新考慮任務板（見 docs/task-board-research.md）。"
                    if hit else "未達觸發線。任務板【先不做】——數字說還不痛。"))


if __name__ == "__main__":
    main()
