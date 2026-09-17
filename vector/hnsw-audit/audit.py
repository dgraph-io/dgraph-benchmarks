"""Self-query orphan audit against a running alpha, for one (index_mode, efc).

For an evenly-spaced sample of loaded vectors, query `similar_to` with the
vector's OWN value and check whether the vector appears in its own top-K.
Classify each sampled vector:

  found   - in top-K at the default efSearch      (normally reachable)
  rescued - only found at the wide EF_RESCUE beam  (reachable but beam-limited)
  absent  - not found even at EF_RESCUE            (truly unfindable: no inbound links)

`absent%` is the headline "unfindable vector" rate. run.sh applies the index
(so the build path is exercised), passes the build time via BUILD_S, and calls
this once per (index_mode, ef_construction) with those as args.
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
    "timestamp", "impl", "dgraph_version", "dataset", "n", "sample",
    "index_mode", "num_clusters", "ef_construction", "ef_search", "ef_rescue",
    "k", "metric", "found", "rescued", "absent",
    "found_pct", "rescued_pct", "absent_pct", "build_s", "desc",
]


def _vs(vec) -> str:
    return "[" + ",".join(f"{x:.6f}" for x in vec) + "]"


def audit(index_mode: str, ef_construction: int) -> dict:
    with h5py.File(hdf5_path(config.dataset), "r") as f:
        n = min(config.n, f["train"].shape[0])
        train = np.asarray(f["train"][:n], dtype=np.float32)

    client = pydgraph.DgraphClient(pydgraph.DgraphClientStub(config.alpha_addr))

    def q(body: str) -> dict:
        txn = client.txn(read_only=True)
        try:
            return json.loads(txn.query(body).json)
        finally:
            txn.discard()

    def self_found(uid: int, vec, extra: str = "") -> bool:
        r = q(f'{{ v(func: similar_to({PRED}, {config.k}, "{_vs(vec)}"{extra})) {{ uid }} }}')
        return uid in {int(x["uid"], 16) for x in r.get("v", [])}

    loaded = q(f"{{ c(func: has({PRED})) {{ count(uid) }} }}")["c"][0]["count"]
    print(f"[audit] {index_mode} efc{ef_construction}: alpha reports {loaded} vectors", flush=True)

    step = max(1, n // config.sample)
    ids = list(range(1, n + 1, step))       # uid = row index + 1
    found = rescued = 0
    absent = []
    t0 = time.time()
    for uid in ids:
        vec = train[uid - 1]
        if self_found(uid, vec):
            found += 1
        elif self_found(uid, vec, f", ef: {config.ef_rescue}"):
            rescued += 1
        else:
            absent.append(hex(uid))
    total = found + rescued + len(absent)
    dt = time.time() - t0
    print(f"[audit] {index_mode} efc{ef_construction}: n={total} "
          f"found={found} rescued={rescued} absent={len(absent)} "
          f"({100 * len(absent) / total:.1f}% unfindable) sweep={dt:.0f}s", flush=True)
    if absent[:10]:
        print(f"[audit] absent uids (first 10): {absent[:10]}", flush=True)

    return {
        "n": n, "sample": total,
        "found": found, "rescued": rescued, "absent": len(absent),
        "found_pct": round(100 * found / total, 2) if total else 0.0,
        "rescued_pct": round(100 * rescued / total, 2) if total else 0.0,
        "absent_pct": round(100 * len(absent) / total, 2) if total else 0.0,
    }


def append_csv(row: dict) -> None:
    os.makedirs(os.path.dirname(RESULTS_CSV), exist_ok=True)
    exists = os.path.exists(RESULTS_CSV)
    with open(RESULTS_CSV, "a", newline="") as f:
        w = csv.DictWriter(f, fieldnames=CSV_FIELDS)
        if not exists:
            w.writeheader()
        w.writerow(row)


def main():
    index_mode = sys.argv[1]
    ef_construction = int(sys.argv[2])
    r = audit(index_mode, ef_construction)
    row = {
        "timestamp": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "impl": "dgraph",
        "dgraph_version": config.dgraph_version,
        "dataset": config.dataset,
        "n": r["n"],
        "sample": r["sample"],
        "index_mode": index_mode,
        "num_clusters": config.num_clusters if index_mode == "partitioned" else "",
        "ef_construction": ef_construction,
        "ef_search": config.hnsw_ef_search,
        "ef_rescue": config.ef_rescue,
        "k": config.k,
        "metric": config.hnsw_metric,
        "found": r["found"],
        "rescued": r["rescued"],
        "absent": r["absent"],
        "found_pct": r["found_pct"],
        "rescued_pct": r["rescued_pct"],
        "absent_pct": r["absent_pct"],
        "build_s": os.getenv("BUILD_S", ""),
        "desc": config.desc,
    }
    append_csv(row)
    print("RESULT " + json.dumps({k: row[k] for k in (
        "index_mode", "ef_construction", "found_pct", "rescued_pct",
        "absent_pct", "build_s")}), flush=True)


if __name__ == "__main__":
    main()
