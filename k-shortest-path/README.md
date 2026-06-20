# k-shortest-path

Local benchmark harness for comparing the four open Dgraph shortest-path
PRs that target [issue #9577](https://github.com/dgraph-io/dgraph/issues/9577)
(k-shortest-path returns incorrect paths when `maxfrontiersize` is hit).

PRs under comparison:

| PR | Family | Approach |
|---|---|---|
| #9576 | Backpressure | `lowWatermark = 0.6 × maxFrontierSize`; stop expanding when reached. Keeps the buggy `pq.Pop()`. |
| #9599 | Fix eviction | `TrimToMax()` (scan + `heap.Remove`), **push-then-trim** + regression unit test. |
| #9607 | Fix eviction | `removeMax()` (scan + `heap.Remove`), check-then-trim. |
| #9678 | Fix eviction | `removeMax()` + push-then-trim + `MaxFrontierSize > 0` guard; 24 unit + 10 integration tests. |

This harness lives **outside** the dgraph repo on purpose — it carries a
multi-GB LDBC Graphalytics dataset that has no business in CI.

The complementary CI-tier benchmark is in
`dgraph/systest/shortest-path/benchmark_test.go` and is wired into
`ci-dgraph-integration2-tests.yml`.

## Layout

```
shortest-path-bench/
├── go.mod
├── docker-compose.yaml         # Alpha + Zero for a single-node cluster
├── scripts/download-ldbc.sh    # fetch a Graphalytics datagen dataset + reference
├── cmd/
│   ├── convert/main.go         # LDBC .v/.e → graph.rdf.gz + graph.schema (for `dgraph bulk`)
│   ├── bench/main.go           # --mode=correctness | --mode=perf
│   └── validate/main.go        # standalone diff vs LDBC SSSP reference (0.01% ε)
├── internal/
│   ├── ldbc/                   # parsers for .v, .e, .properties, validation/.SSSP
│   ├── client/                 # dgo client wrapper
│   └── stats/                  # latency histogram, p50/p95/p99
└── results/                    # per-PR JSON outputs (gitignored)
```

The loader pipeline is built around `dgraph bulk`, not live-load: at the L
scale (~34M edges) live-load would take hours. `cmd/convert` emits a
blank-node RDF file + schema that `dgraph bulk` consumes in one pass.

## Prerequisites

- Docker + Docker Compose
- Go 1.26+
- ~10 GB free disk for a single LDBC datagen-7_5-fb dataset
- A built `dgraph` binary on `$PATH` (the docker-compose pulls the standard
  image; build a custom one per PR — see below)

## Per-PR workflow

Each PR run is one full pass through these steps. Results land in
`results/<pr-id>/` and are aggregated with `compare`.

```bash
# 0. one-time: pull and extract a Graphalytics dataset
./scripts/download-ldbc.sh datagen-7_5-fb

# 1. check out the PR you want to benchmark
cd ~/workspace/dgraph
git fetch origin pull/9607/head:pr-9607 && git checkout pr-9607
make docker-image                  # tags dgraph/dgraph:local

# 2. convert LDBC files to Dgraph RDF + schema
cd ~/workspace/shortest-path-bench
go run ./cmd/convert -dataset ./datasets/datagen-7_5-fb
# → writes datasets/datagen-7_5-fb/dgraph/{graph.rdf.gz, graph.schema}

# 3. bulk-load into a Zero, producing the alpha data directory
docker compose up -d zero
dgraph bulk \
    -f datasets/datagen-7_5-fb/dgraph/graph.rdf.gz \
    -s datasets/datagen-7_5-fb/dgraph/graph.schema \
    --zero localhost:5080 \
    --out datasets/datagen-7_5-fb/dgraph/bulk-out
docker compose down

# 4. bring up Alpha with the bulk-loaded data
cp -r datasets/datagen-7_5-fb/dgraph/bulk-out/0/p ./data-p
DGRAPH_IMAGE=dgraph/dgraph:local DATA_DIR=$(pwd)/data-p docker compose up -d

# 5a. correctness — sample N targets, diff against LDBC reference
go run ./cmd/bench \
    -mode correctness \
    -dataset ./datasets/datagen-7_5-fb \
    -alpha localhost:9080 \
    -targets 1000 \
    -maxfrontier 1000 \
    -out results/pr-9607/correctness.json

# 5b. perf — sampled (src,dst) pairs at varying concurrency
go run ./cmd/bench \
    -mode perf \
    -dataset ./datasets/datagen-7_5-fb \
    -alpha localhost:9080 \
    -pairs 10000 \
    -concurrency 16 \
    -maxfrontier 1000 \
    -out results/pr-9607/perf.json

# 6. aggregate the per-PR JSON files into a verdict matrix by hand or with
# a small jq/python script over results/*/*.json — left intentionally
# un-automated for now since the comparison is a one-shot exercise.
```

## Output

### correctness.json
```jsonc
{
    "dataset": "datagen-7_5-fb",
    "source_vertex": 1,
    "targets_sampled": 1000,
    "epsilon": 0.0001,
    "passed": 982,
    "failed": 18,
    "infinity_mismatches": 0,
    "first_failures": [
        {"vertex": 12345, "expected": 4.32, "got": 4.71, "rel_error": 0.0903}
    ]
}
```

### perf.json
```jsonc
{
    "dataset": "datagen-7_5-fb",
    "pairs": 10000,
    "concurrency": 16,
    "maxfrontier": 1000,
    "latency_ms": {"p50": 4.1, "p95": 18.3, "p99": 42.7},
    "qps": 1820,
    "heap_alloc_peak_mb": 612,
    "errors": 0
}
```

## How to read the verdict

The interesting comparisons:

- **Family A (#9599/#9607/#9678)** should all pass correctness; if any fails,
  that PR has a latent bug. Within family A, p50/p95 diffs are mostly noise
  unless the push-vs-check ordering matters at higher cap utilisation.
- **Family B (#9576)** is the discriminator: backpressure throttles
  exploration, so it may show **better latency at the cap** but also a
  higher correctness-failure count. The verdict depends on whether the
  user-visible promise of `shortest` is "fastest answer" or "optimal answer".

If correctness pass-rate is < 99% on any PR for a workload that real users
hit, that's a merge blocker regardless of perf.
