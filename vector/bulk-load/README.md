# Bulk-load Vector Benchmark for Dgraph

Measures the **offline bulk loader** building a vector index: build wall-clock,
peak build memory, output size, and query **recall@K** — comparing the standard
`hnsw` index against the partitioned (`numClusters`, IVF-over-HNSW) index on the
same data.

This complements `../beir/`, which measures *live-insert* + retrieval quality;
this suite measures the *bulk build* path.

## Prerequisites

- **Docker** (running)
- **uv** — `curl -LsSf https://astral.sh/uv/install.sh | sh`
- Disk for the dataset (GIST is ~3.6 GB, downloaded once and cached in `./data/`)
- **A Dgraph build whose bulk loader builds vector indexes** — see below.

> [!IMPORTANT]
> **The offline bulk loader must build the HNSW/partitioned index.** This is
> what the benchmark measures — and it is a **recent, currently-unreleased**
> capability. A released image (e.g. `v25.2.0`) *stores* the vectors during
> `dgraph bulk` but does **not** build the graph, so `similar_to` returns ~1
> neighbor and recall comes out ~0. `bench.py` prints a loud `[WARN]` when it
> detects this. Use `DGRAPH_VERSION=local`, built from a branch that has bulk
> vector-index support (grep `dgraph/cmd/bulk/reduce.go` for `vectorIndexSpecs`).

To test a local Dgraph build, build the image first and set `DGRAPH_VERSION=local`:

```bash
# in your dgraph checkout
make docker-image        # produces dgraph/dgraph:local
```

> [!WARNING]
> `make docker-image` copies the **host-built** `dgraph` binary into a Linux
> image. Run it on a machine whose OS/arch matches your Docker engine (i.e. a
> Linux host). Building on macOS drops a Mach-O (darwin) binary into a Linux
> image, and every container dies with `exec format error`. On macOS, build the
> binary inside a `golang` container (linux) before packaging.

## Quick start

```bash
./run.sh
```

That single command:
1. `uv sync` — installs Python deps into a local venv
2. downloads the dataset (cached in `./data/`) and materializes RDF + schema in `./work/`
3. for each index config (`hnsw`, then `partitioned`):
   - bulk-loads with `dgraph bulk`, sampling peak memory
   - serves the output with an alpha
   - measures recall@K against brute-force ground truth
4. appends one row per config to `results/bulk_results.csv`

Override any setting inline (takes precedence over `.env`):

```bash
N=1000000 INDEX_MODES=partitioned ./run.sh      # full 1M, partitioned only
DGRAPH_VERSION=v25.1.0 ./run.sh                  # a published release
DATASET=sift-128-euclidean ./run.sh              # smaller/faster dataset
```

## Configuration

Copy and edit `.env` (auto-created from `.env.example` on first run). Key knobs:

| Variable | Default | Meaning |
|---|---|---|
| `DGRAPH_VERSION` | `local` | image tag; `local` or e.g. `v25.1.0` |
| `DATASET` | `gist-960-euclidean` | see `datasets.py` registry |
| `N` | `200000` | vectors to load (sets go up to 1,000,000) |
| `INDEX_MODES` | `hnsw,partitioned` | which configs to run |
| `HNSW_EF_CONSTRUCTION` | `64` | build-time beam width |
| `HNSW_EF_SEARCH` | `32` | query-time beam width |
| `NUM_CLUSTERS` | auto `~N/450` | partitioned index cluster count |
| `BULK_THREADS` | `8` | `dgraph bulk -j` |

## Datasets

ANN-benchmarks HDF5 files (a `train` corpus + `test` queries), downloaded from
hardcoded URLs in `datasets.py` — pick a name, the code fetches it:

| `DATASET` | Vectors × dim | Size |
|---|---|---|
| `gist-960-euclidean` | 1,000,000 × 960 | ~3.6 GB |
| `sift-128-euclidean` | 1,000,000 × 128 | ~0.5 GB |

Add one by adding a single `REGISTRY` entry.

## Output

`results/bulk_results.csv`, one row per (config, run):

```
index_mode, n, bulk_wall_s, peak_rss_mb, pdir_mb, recall_at_k, query_p50_ms, query_p95_ms, ...
```

## Notes

- Ground truth is computed by brute force over the exact loaded subset (the
  HDF5's shipped neighbors are for the full 1M set and would be wrong for N < 1M).
- Recall for the partitioned index is expected to be lower than exact at a small
  `numProbes`/`efSearch` — it trades some recall for much faster builds; that
  trade-off is the point of the comparison.
- Everything under `data/` and `work/` is generated and gitignored.
