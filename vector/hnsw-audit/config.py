"""Configuration for the HNSW unfindable-vector audit benchmark.

All settings come from environment variables (loaded from .env by run.sh or
python-dotenv), with defaults so a first run needs no configuration.
Mirrors vector/beir/config.py and ../bulk-load/config.py.
"""

import os
import sys

from dotenv import load_dotenv

load_dotenv()

# The predicate every vector is stored under.
PRED = "embedding"

# Paths, all relative to this benchmark directory (never absolute / personal).
HERE = os.path.dirname(os.path.abspath(__file__))
DATA_DIR = os.path.join(HERE, "data")       # downloaded datasets (gitignored)
WORK_DIR = os.path.join(HERE, "work")       # generated RDF (gitignored)
RESULTS_CSV = os.path.join(HERE, "results", "audit_results.csv")


def _int(name: str, default: int) -> int:
    v = os.getenv(name)
    return int(v) if v not in (None, "") else default


def _list(name: str, default: str):
    return [x.strip() for x in os.getenv(name, default).split(",") if x.strip()]


class Config:
    # Which dgraph build to test (see docker-compose.yml). "local" uses a
    # locally built dgraph/dgraph:local image.
    dgraph_image = os.getenv("DGRAPH_IMAGE", "dgraph/dgraph")
    dgraph_version = os.getenv("DGRAPH_VERSION", "local")
    alpha_addr = os.getenv("ALPHA_ADDR", "localhost:9080")

    # Dataset + scale. DATASET names an entry in datasets.py's registry.
    dataset = os.getenv("DATASET", "gist-960-euclidean")
    n = _int("N", 200_000)              # vectors loaded
    sample = _int("SAMPLE", 1000)       # vectors audited (evenly spaced across N)
    k = _int("K", 100)                  # self-query top-K

    # Implementations compared: hnsw, partitioned (dgraph) and hnswlib (reference).
    index_modes = _list("INDEX_MODES", "hnsw,partitioned,hnswlib")
    # efConstruction values swept — the key lever for the orphan rate.
    ef_construction_list = [int(x) for x in _list("EF_CONSTRUCTION_LIST", "64,200")]

    hnsw_metric = os.getenv("HNSW_METRIC", "euclidean")
    hnsw_max_levels = _int("HNSW_MAX_LEVELS", 3)
    hnsw_ef_search = _int("HNSW_EF_SEARCH", 32)   # default query beam
    ef_rescue = _int("EF_RESCUE", 2000)           # wide beam: separates orphaned from beam-limited
    hnsw_m = _int("HNSW_M", 64)                   # hnswlib M, matched to dgraph's neighbor-row cap

    # Partitioned (IVF) cluster count. If unset, derived from N (~N/450 per cluster).
    num_clusters = _int("NUM_CLUSTERS", max(4, n // 450))

    live_threads = _int("LIVE_THREADS", 8)
    desc = os.getenv("DESC", "")

    def raw_schema(self) -> str:
        # No index: load raw vectors first, then alter to add the index so the
        # build runs on the (re)index path the audit targets.
        return f"{PRED}: float32vector .\n"

    def schema(self, index_mode: str, ef_construction: int) -> str:
        opts = (f'metric:"{self.hnsw_metric}",maxLevels:"{self.hnsw_max_levels}",'
                f'efSearch:"{self.hnsw_ef_search}",efConstruction:"{ef_construction}"')
        if index_mode == "partitioned":
            opts += f',numClusters:"{self.num_clusters}"'
        return f"{PRED}: float32vector @index(hnsw({opts})) .\n"


config = Config()


if __name__ == "__main__":
    # run.sh pulls schema strings from here so schema logic has one home:
    #   python config.py raw-schema
    #   python config.py schema <mode> <ef_construction>
    if len(sys.argv) >= 2 and sys.argv[1] == "raw-schema":
        sys.stdout.write(config.raw_schema())
    elif len(sys.argv) >= 4 and sys.argv[1] == "schema":
        sys.stdout.write(config.schema(sys.argv[2], int(sys.argv[3])))
    else:
        sys.exit("usage: config.py raw-schema | schema <mode> <ef_construction>")
