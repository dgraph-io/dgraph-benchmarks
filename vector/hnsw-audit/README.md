# HNSW Unfindable-Vector Audit for Dgraph

Measures how many indexed vectors are **unfindable** — a vector that, when you
query the index with its *own* value, is not returned even in a wide search.
Such a vector has no inbound graph links, so no search can ever reach it. This
audits Dgraph's `hnsw` and partitioned (`numClusters`) indexes against
**hnswlib**, a correct reference HNSW, on identical data at matched parameters.

This complements `../beir/` (retrieval quality) and `../bulk-load/` (build
speed/recall): this suite isolates a specific **index-health** defect.

## What it measures

For an evenly-spaced sample of the loaded vectors, each is self-queried and
classified:

| class | meaning |
|---|---|
| **found** | in its own top-K at the default `efSearch` — normally reachable |
| **rescued** | found only at the wide `EF_RESCUE` beam — reachable but beam-limited |
| **absent** | not found even at `EF_RESCUE` — **truly unfindable** (no inbound links) |

`absent%` is the headline number. hnswlib's `absent%` is the achievable floor on
the same data; Dgraph's number is read against it.

## Prerequisites

- **Docker** (running)
- **uv** — `curl -LsSf https://astral.sh/uv/install.sh | sh`
- Disk for the dataset (GIST is ~3.6 GB, downloaded once and cached in `./data/`)

> [!IMPORTANT]
> Use a Dgraph build whose loader builds the vector index (currently an
> unreleased capability — see `../bulk-load/README.md`). Build it with
> `make docker-image` **on a host whose OS/arch matches your Docker engine**
> (a macOS-built binary in a Linux image dies with `exec format error`; on macOS
> build the binary inside a `golang` container first), then set
> `DGRAPH_VERSION=local`.

## Quick start

```bash
./run.sh
```

That single command:
1. `uv sync` — installs Python deps into a local venv
2. downloads the dataset (cached in `./data/`) and materializes RDF in `./work/`
3. for each `EF_CONSTRUCTION_LIST` value × each dgraph mode (`hnsw`, `partitioned`):
   - live-loads N vectors, then adds the index (exercising the build path)
   - self-queries `SAMPLE` vectors and classifies found / rescued / absent
4. runs the **hnswlib** baseline in-process at matched params
5. appends one row per (impl, mode, efConstruction) to `results/audit_results.csv`

Override any setting inline (takes precedence over `.env`):

```bash
INDEX_MODES=hnsw EF_CONSTRUCTION_LIST=64 ./run.sh   # just dgraph hnsw at efc 64
DATASET=sift-128-euclidean N=100000 ./run.sh        # smaller/faster
```

## Configuration

Copy and edit `.env` (auto-created from `.env.example` on first run). Key knobs:

| Variable | Default | Meaning |
|---|---|---|
| `DGRAPH_VERSION` | `local` | image tag; `local` or e.g. `v25.1.0` |
| `DATASET` | `gist-960-euclidean` | see `datasets.py` registry |
| `N` | `200000` | vectors to load |
| `SAMPLE` | `1000` | vectors audited |
| `K` | `100` | self-query top-K |
| `INDEX_MODES` | `hnsw,partitioned,hnswlib` | which implementations to run |
| `EF_CONSTRUCTION_LIST` | `64,200` | efConstruction values swept |
| `HNSW_EF_SEARCH` | `32` | default query beam |
| `EF_RESCUE` | `2000` | wide beam separating orphaned from beam-limited |
| `HNSW_M` | `64` | hnswlib M, matched to dgraph's neighbor-row cap |

## Output

`results/audit_results.csv`, one row per (impl, index_mode, efConstruction):

```
impl, index_mode, ef_construction, found_pct, rescued_pct, absent_pct, build_s, ...
```

## Notes

- **hnswlib advantage, reported as-is:** hnswlib caps layer 0 at `2*M` neighbors,
  where Dgraph caps a single neighbor row at `M`. That structural difference is
  part of what the comparison surfaces; it is not corrected for.
- The default beams differ slightly by construction (dgraph uses schema
  `efSearch`; hnswlib requires `ef >= K`), so compare `absent%` — measured at the
  same wide `EF_RESCUE` for both — as the like-for-like number.
- Everything under `data/` and `work/` is generated and gitignored.
