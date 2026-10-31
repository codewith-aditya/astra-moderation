#!/usr/bin/env python3
"""Evaluate astra-moderation against a labelled image set.

Usage:
  python3 tools/eval.py --base http://localhost:8096 --key amk_... \
      --pos positives/ --neg negatives/ --out results.csv

Positives = images that should be flagged as NSFW, negatives = benign.
Score per image = max(nudity, sexual_content, suggestive).
Requires only the Python standard library.
"""
import argparse
import base64
import csv
import json
import mimetypes
import os
import sys
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor

THRESHOLDS = (0.3, 0.4, 0.5, 0.8)
SCORE_FIELDS = ("nudity", "sexual_content", "suggestive")


def image_files(folder):
    out = []
    for name in sorted(os.listdir(folder)):
        path = os.path.join(folder, name)
        if os.path.isfile(path) and mimetypes.guess_type(path)[0] in (
            "image/jpeg", "image/png", "image/webp",
        ):
            out.append(path)
    return out


def data_url(path):
    mime = mimetypes.guess_type(path)[0] or "image/jpeg"
    with open(path, "rb") as fh:
        raw = base64.b64encode(fh.read()).decode()
    return f"data:{mime};base64,{raw}"


def moderate(base, key, path, timeout):
    body = json.dumps({"image_url": data_url(path)}).encode()
    req = urllib.request.Request(
        base.rstrip("/") + "/v1/moderate",
        data=body,
        headers={"Content-Type": "application/json", "X-API-Key": key},
        method="POST",
    )
    start = time.time()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        payload = json.loads(resp.read())
    latency = time.time() - start
    cats = payload.get("categories") or {}
    score = max(float(cats.get(f, 0.0)) for f in SCORE_FIELDS)
    return {
        "file": os.path.basename(path),
        "score": score,
        "verdict": payload.get("verdict"),
        "latency": latency,
        "categories": cats,
    }


def auc(pos, neg):
    wins = 0.0
    for p in pos:
        for n in neg:
            wins += 1.0 if p > n else 0.5 if p == n else 0.0
    return wins / (len(pos) * len(neg))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://localhost:8096")
    ap.add_argument("--key", required=True)
    ap.add_argument("--pos", required=True, help="folder with NSFW images")
    ap.add_argument("--neg", required=True, help="folder with benign images")
    ap.add_argument("--out", default="results.csv")
    ap.add_argument("--concurrency", type=int, default=4)
    ap.add_argument("--timeout", type=int, default=180)
    args = ap.parse_args()

    pos_files = image_files(args.pos)
    neg_files = image_files(args.neg)
    if not pos_files or not neg_files:
        sys.exit("empty pos/neg folder")

    tasks = [(1, p) for p in pos_files] + [(0, p) for p in neg_files]
    rows = []

    def run(item):
        label, path = item
        try:
            result = moderate(args.base, args.key, path, args.timeout)
            result["label"] = label
            return result
        except Exception as exc:  # noqa: BLE001
            return {"label": label, "file": os.path.basename(path), "error": str(exc)}

    with ThreadPoolExecutor(max_workers=max(1, args.concurrency)) as pool:
        for i, row in enumerate(pool.map(run, tasks), 1):
            rows.append(row)
            print(f"  {i}/{len(tasks)} {row['file']}: {row.get('error') or round(row.get('score', 0), 3)}")

    with open(args.out, "w", newline="") as fh:
        writer = csv.DictWriter(fh, fieldnames=["label", "file", "score", "verdict", "latency", "error"])
        writer.writeheader()
        for row in rows:
            writer.writerow({k: row.get(k) for k in writer.fieldnames})

    ok = [r for r in rows if "error" not in r]
    pos = [r["score"] for r in ok if r["label"] == 1]
    neg = [r["score"] for r in ok if r["label"] == 0]
    print(f"\nok={len(ok)} errors={len(rows)-len(ok)}")
    if not pos or not neg:
        sys.exit("not enough data for metrics")
    lat = sorted(r["latency"] for r in ok)
    print(f"latency p50={lat[len(lat)//2]:.2f}s p90={lat[int(len(lat)*0.9)]:.2f}s")
    print(f"AUC={auc(pos, neg):.3f}")
    for th in THRESHOLDS:
        tp = sum(1 for s in pos if s >= th)
        fn = len(pos) - tp
        fp = sum(1 for s in neg if s >= th)
        precision = tp / (tp + fp) if tp + fp else 0.0
        recall = tp / (tp + fn) if tp + fn else 0.0
        f1 = 2 * precision * recall / (precision + recall) if precision + recall else 0.0
        print(f"th={th}: P={precision:.2f} R={recall:.2f} F1={f1:.2f} (tp{tp} fp{fp} fn{fn})")


if __name__ == "__main__":
    main()
