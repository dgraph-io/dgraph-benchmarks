"""Configuration for the bulk-load vector benchmark.

All settings come from environment variables (loaded from .env by run.sh or
python-dotenv), with sensible defaults so a first run needs no configuration.
Mirrors vector/beir/config.py.
"""

import os

from dotenv import load_dotenv

load_dotenv()

# The predicate every vector is stored under.
PRED = "embedding"

# Paths, all relative to this benchmark directory (never absolute / personal).
HERE = os.path.dirname(os.path.abspath(__file__))
DATA_DIR = os.path.join(HERE, "data")       # downloaded datasets (gitignored)
WORK_DIR = os.path.join(HERE, "work")       # generated RDF/schema (gitignored)
RESULTS_CSV = os.path.join(HERE, "results", "bulk_results.csv")


def _int(name: str, default: int) -> int:
    v = os.getenv(name)
    return int(v) if v not in (None, "") else default


class Config:
    # Which dgraph build to test (see docker-compose.yml). "local" uses a
    # locally built dgraph/dgraph:local image.
    dgraph_image = os.getenv("DGRAPH_IMAGE", "dgraph/dgraph")
    dgraph_version = os.getenv("DGRAPH_VERSION", "local")
    alpha_addr = os.getenv("ALPHA_ADDR", "localhost:9080")

    # Dataset + scale. DATASET names an entry in datasets.py's registry.
    dataset = os.getenv("DATASET", "gist-960-euclidean")
    n = _int("N", 200_000)                   # vectors to load
    num_queries = _int("NUM_QUERIES", 200)
    k = _int("K", 10)

    # Index configs to run, comma-separated: "hnsw" and/or "partitioned".
    index_modes = [m.strip() for m in os.getenv("INDEX_MODES", "hnsw,partitioned").split(",") if m.strip()]

    # HNSW parameters (shared by both index modes).
    hnsw_metric = os.getenv("HNSW_METRIC", "euclidean")
    hnsw_max_levels = _int("HNSW_MAX_LEVELS", 3)
    hnsw_ef_search = _int("HNSW_EF_SEARCH", 32)
    hnsw_ef_construction = _int("HNSW_EF_CONSTRUCTION", 64)

    # Partitioned (IVF) cluster count. If unset, a reasonable default is derived
    # from N so clustering stays meaningful (~N/450 vectors per cluster).
    num_clusters = _int("NUM_CLUSTERS", max(4, n // 450))

    bulk_threads = _int("BULK_THREADS", 8)
    desc = os.getenv("DESC", "")

    def schema(self, index_mode: str) -> str:
        opts = (f'metric:"{self.hnsw_metric}",maxLevels:"{self.hnsw_max_levels}",'
                f'efSearch:"{self.hnsw_ef_search}",efConstruction:"{self.hnsw_ef_construction}"')
        if index_mode == "partitioned":
            opts += f',numClusters:"{self.num_clusters}"'
        return f'{PRED}: float32vector @index(hnsw({opts})) .\n'


config = Config()
