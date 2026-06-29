#!/usr/bin/env python3
"""取官方 SWE-bench Lite 子集 → JSONL，供 cogito `cmd/bench -swebench` 使用。

走 HuggingFace datasets-server REST API（只用 stdlib，無需 pip install datasets）。
用法：
    python3 scripts/fetch_swebench_lite.py -n 10 -o lite.jsonl
    python3 scripts/fetch_swebench_lite.py -n 5 --offset 150 -o requests.jsonl   # 取某段（如 psf/requests）
"""
import argparse
import json
import urllib.parse
import urllib.request

API = "https://datasets-server.huggingface.co/rows"
DATASET = "princeton-nlp/SWE-bench_Lite"
FIELDS = [
    "instance_id", "repo", "base_commit", "problem_statement",
    "patch", "test_patch", "FAIL_TO_PASS", "PASS_TO_PASS", "version",
]


def fetch(offset, length, split):
    q = urllib.parse.urlencode({
        "dataset": DATASET, "config": "default", "split": split,
        "offset": offset, "length": length,
    })
    with urllib.request.urlopen(f"{API}?{q}", timeout=60) as r:
        return json.load(r).get("rows", [])


def main():
    ap = argparse.ArgumentParser(description="Fetch SWE-bench Lite subset as JSONL")
    ap.add_argument("-n", "--num", type=int, default=10, help="要取幾題")
    ap.add_argument("-o", "--out", default="lite.jsonl", help="輸出檔")
    ap.add_argument("--offset", type=int, default=0, help="從第幾筆開始（資料按 instance_id 字母排序）")
    ap.add_argument("--split", default="test")
    a = ap.parse_args()

    rows, off = [], a.offset
    while len(rows) < a.num:
        chunk = fetch(off, min(100, a.num - len(rows)), a.split)  # API 單次上限 100
        if not chunk:
            break
        rows += chunk
        off += len(chunk)

    with open(a.out, "w", encoding="utf-8") as f:
        for item in rows[:a.num]:
            row = item["row"]
            f.write(json.dumps({k: row.get(k) for k in FIELDS}, ensure_ascii=False) + "\n")
    print(f"✅ 寫入 {min(len(rows), a.num)} 題 → {a.out}（接著：go run ./cmd/bench -swebench {a.out} -dry-run）")


if __name__ == "__main__":
    main()
