"""Recall + latency measurement for a bulk-loaded alpha, and CSV output.

run.sh calls this AFTER the alpha is serving the bulk output. Build-phase
metrics (wall time, peak RSS, p-dir size) are measured by run.sh around the
bulk container and passed in via env; this script computes recall@K against
brute-force ground truth, then appends one complete row to results/bulk_results.csv.
"""

import csv
import json
import os
import sys
import time
from datetime import datetime, timezone

import h5py
import numpy as np
import pydgraph

from config import PRED, RESULTS_CSV, config
from datasets import hdf5_path

CSV_FIELDS = [
    "timestamp", "dgraph_version", "dataset", "n", "index_mode", "num_clusters",
    "ef_construction", "ef_search", "metric",
    "bulk_wall_s", "peak_rss_mb", "pdir_mb",
    "recall_at_k", "k", "query_p50_ms", "query_p95_ms", "avg_returned",
    "num_queries", "desc",
]


def measure_recall(train, tests, k, alpha_addr):
    """Brute-force euclidean ground truth over the loaded subset, then query alpha."""
    print(f"[recall] brute-force truth: {len(tests)} queries x {len(train)} vectors", flush=True)
    train_sq = np.einsum("ij,ij->i", train, train)  # ||x||^2 per row
    truth = []
    for q in tests:
        d = train_sq - 2.0 * (train @ q)  # argmin of ||x-q||^2 (||q||^2 constant per query)
        truth.append(set(np.argpartition(d, k)[:k].tolist()))

    client = pydgraph.DgraphClient(pydgraph.DgraphClientStub(alpha_addr))
    hits, total, lat, returned = 0, 0, [], 0
    for qi, vec in enumerate(tests):
        v = "[" + ",".join(f"{x:.6f}" for x in vec) + "]"
        query = f'{{ q(func: similar_to({PRED}, {k}, "{v}")) {{ uid }} }}'
        t0 = time.time()
        resp = client.txn(read_only=True).query(query)
        lat.append(time.time() - t0)
        got = {int(r["uid"], 16) - 1 for r in json.loads(resp.json).get("q", [])}
        returned += len(got)
        hits += len(got & truth[qi])
        total += k
    lat.sort()
    avg_returned = returned / len(tests) if len(tests) else 0
    # An unbuilt index answers similar_to with ~1 node (just the entry point):
    # the tell-tale of a dgraph build whose bulk loader doesn't construct the
    # vector graph. Surface it loudly so nobody debugs a "correct" 0.0 recall.
    if avg_returned < max(2, k / 2):
        print(
            f"[WARN] similar_to returned {avg_returned:.1f}/{k} neighbors on average "
            f"-- the vector index looks UNBUILT.\n"
            f"       The offline bulk loader only builds HNSW/partitioned indexes in "
            f"dgraph builds that support it (currently unreleased). A released image "
            f"stores the vectors but not the graph, giving ~0 recall.\n"
            f"       Fix: run with DGRAPH_VERSION=local built from a branch with bulk "
            f"vector-index support (see README).",
            flush=True,
        )
    return {
        "recall_at_k": round(hits / total, 4) if total else 0.0,
        "query_p50_ms": round(lat[len(lat) // 2] * 1000, 1),
        "query_p95_ms": round(lat[min(int(len(lat) * 0.95), len(lat) - 1)] * 1000, 1),
        "avg_returned": round(avg_returned, 1),
    }


def append_csv(row):
    os.makedirs(os.path.dirname(RESULTS_CSV), exist_ok=True)
    exists = os.path.exists(RESULTS_CSV)
    with open(RESULTS_CSV, "a", newline="") as f:
        w = csv.DictWriter(f, fieldnames=CSV_FIELDS)
        if not exists:
            w.writeheader()
        w.writerow(row)


def main():
    index_mode = sys.argv[1] if len(sys.argv) > 1 else config.index_modes[0]

    with h5py.File(hdf5_path(config.dataset), "r") as f:
        n = min(config.n, f["train"].shape[0])
        train = np.asarray(f["train"][:n], dtype=np.float32)
        tests = np.asarray(f["test"][:config.num_queries], dtype=np.float32)

    rec = measure_recall(train, tests, config.k, config.alpha_addr)

    row = {
        "timestamp": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "dgraph_version": config.dgraph_version,
        "dataset": config.dataset,
        "n": n,
        "index_mode": index_mode,
        "num_clusters": config.num_clusters if index_mode == "partitioned" else "",
        "ef_construction": config.hnsw_ef_construction,
        "ef_search": config.hnsw_ef_search,
        "metric": config.hnsw_metric,
        "bulk_wall_s": os.getenv("BULK_WALL_S", ""),
        "peak_rss_mb": os.getenv("PEAK_RSS_MB", ""),
        "pdir_mb": os.getenv("PDIR_MB", ""),
        "k": config.k,
        "num_queries": len(tests),
        "desc": config.desc,
        **rec,
    }
    append_csv(row)
    print("RESULT " + json.dumps({k: row[k] for k in (
        "index_mode", "n", "bulk_wall_s", "peak_rss_mb", "pdir_mb",
        "recall_at_k", "query_p50_ms", "query_p95_ms")}), flush=True)


if __name__ == "__main__":
    main()
