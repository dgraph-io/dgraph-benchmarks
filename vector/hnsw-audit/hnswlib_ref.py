"""hnswlib reference audit — the achievable orphan floor on this exact data.

Builds an hnswlib index in-process (no dgraph) over the same N vectors, at
M = HNSW_M (matched to dgraph's neighbor-row cap) and the same efConstruction
sweep, then runs the identical self-in-own-top-K classification. hnswlib is a
correct, widely-used HNSW; its `absent%` is what a healthy index achieves, so
it is the baseline dgraph's numbers are read against.

Note (reported as-is): hnswlib gives layer 0 a cap of 2*M neighbors, an inherent
structural advantage over a single M-capped row. See README.
"""

import time
from datetime import datetime, timezone

import h5py
import numpy as np

from audit import CSV_FIELDS, append_csv  # single CSV schema + writer
from config import config
from datasets import hdf5_path

_SPACE = {"euclidean": "l2", "angular": "cosine", "cosine": "cosine"}


def _hnswlib_version() -> str:
    try:
        from importlib.metadata import version
        return version("hnswlib")
    except Exception:
        return "?"


def main():
    import hnswlib

    with h5py.File(hdf5_path(config.dataset), "r") as f:
        n = min(config.n, f["train"].shape[0])
        train = np.asarray(f["train"][:n], dtype=np.float32)
    dim = train.shape[1]

    step = max(1, n // config.sample)
    ids = np.arange(0, n, step)[:config.sample]     # 0-based rows sampled
    qs = train[ids]
    space = _SPACE.get(config.hnsw_metric, "l2")

    for efc in config.ef_construction_list:
        idx = hnswlib.Index(space=space, dim=dim)
        idx.init_index(max_elements=n, ef_construction=efc, M=config.hnsw_m)
        t0 = time.time()
        idx.add_items(train, np.arange(n), num_threads=config.live_threads)
        build_s = round(time.time() - t0)

        # hnswlib requires ef >= k; default beam mirrors dgraph's efSearch.
        idx.set_ef(max(config.hnsw_ef_search, config.k))
        lab_def, _ = idx.knn_query(qs, k=config.k, num_threads=config.live_threads)
        idx.set_ef(max(config.ef_rescue, config.k))
        lab_res, _ = idx.knn_query(qs, k=config.k, num_threads=config.live_threads)

        found_def = np.array([ids[i] in lab_def[i] for i in range(len(ids))])
        found_res = np.array([ids[i] in lab_res[i] for i in range(len(ids))])
        found = int(found_def.sum())
        rescued = int((found_res & ~found_def).sum())
        absent = int((~found_res).sum())
        total = len(ids)
        print(f"[hnswlib] M{config.hnsw_m} efc{efc}: n={total} "
              f"found={found} rescued={rescued} absent={absent} "
              f"({100 * absent / total:.1f}% unfindable) build={build_s}s", flush=True)

        append_csv({
            "timestamp": datetime.now(timezone.utc).isoformat(timespec="seconds"),
            "impl": "hnswlib",
            "dgraph_version": f"hnswlib-{_hnswlib_version()}",
            "dataset": config.dataset,
            "n": n,
            "sample": total,
            "index_mode": "hnswlib",
            "num_clusters": "",
            "ef_construction": efc,
            "ef_search": config.hnsw_ef_search,
            "ef_rescue": config.ef_rescue,
            "k": config.k,
            "metric": config.hnsw_metric,
            "found": found,
            "rescued": rescued,
            "absent": absent,
            "found_pct": round(100 * found / total, 2) if total else 0.0,
            "rescued_pct": round(100 * rescued / total, 2) if total else 0.0,
            "absent_pct": round(100 * absent / total, 2) if total else 0.0,
            "build_s": build_s,
            "desc": config.desc,
        })


if __name__ == "__main__":
    main()
