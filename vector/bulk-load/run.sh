#!/bin/bash
# Bulk-load vector benchmark: for each index config (hnsw, partitioned),
# bulk-load N vectors, measure build wall time + peak RSS + p-dir size, serve
# them, and measure recall@K vs brute-force ground truth. One command, only
# Docker + uv required. Config lives in .env (copied from .env.example).
set -euo pipefail
cd "$(dirname "$0")"

GREEN='\033[0;32m'; RED='\033[0;31m'; NC='\033[0m'
info() { echo -e "${GREEN}[INFO]${NC} $1"; }
err()  { echo -e "${RED}[ERROR]${NC} $1"; }

command -v uv >/dev/null || { err "uv not installed: curl -LsSf https://astral.sh/uv/install.sh | sh"; exit 1; }
docker info >/dev/null 2>&1 || { err "Docker is not running."; exit 1; }
DC="docker compose"; $DC version >/dev/null 2>&1 || DC="docker-compose"

# Load .env without overriding anything already in the environment (so
# `N=1000000 ./run.sh` works). Mirrors vector/beir/run_benchmark.sh.
[ -f .env ] || { info ".env not found; copying .env.example"; cp .env.example .env; }
while IFS='=' read -r key value; do
  [[ -z "$key" || "$key" =~ ^# ]] && continue
  key=$(echo "$key" | xargs); value=$(echo "$value" | sed 's/#.*$//' | xargs)
  [ -z "${!key+x}" ] && export "$key=$value"
done < .env

: "${DGRAPH_VERSION:=local}"; export DGRAPH_VERSION
: "${N:=200000}"; : "${BULK_THREADS:=8}"; : "${INDEX_MODES:=hnsw,partitioned}"
: "${DATASET:=gist-960-euclidean}"

# Preflight: fail fast with an actionable message if the image is unavailable,
# instead of a cryptic docker error after uv sync + a multi-GB download.
IMAGE="${DGRAPH_IMAGE:-dgraph/dgraph}:${DGRAPH_VERSION}"
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  if [ "$DGRAPH_VERSION" = "local" ]; then
    err "Image '$IMAGE' not found. This benchmark needs a Dgraph build whose"
    err "bulk loader builds vector indexes (see README). Build one in your dgraph"
    err "checkout ('make docker-image'), or set DGRAPH_VERSION to a published tag."
    exit 1
  fi
  info "Image '$IMAGE' not present locally; Docker will pull it on first use."
fi

info "Installing Python deps (uv sync)..."
uv sync --quiet

info "Preparing dataset $DATASET (N=$N) — downloads on first run, then materializes RDF..."
uv run python datasets.py "$DATASET" "$N"

wait_health() { # $1=url $2=label
  for _ in $(seq 1 120); do curl -sf "$1" >/dev/null 2>&1 && return 0; sleep 2; done
  err "$2 not healthy"; return 1
}

# Convert a `docker stats` MemUsage token (e.g. "45.6MiB", "1.2GiB") to KiB,
# in pure awk so we depend only on coreutils present everywhere (no GNU numfmt,
# which macOS lacks). Unknown/empty input prints 0.
mem_to_kb() {
  awk -v s="$1" 'BEGIN {
    n = s + 0
    if (s ~ /GiB/) print int(n * 1024 * 1024)
    else if (s ~ /MiB/) print int(n * 1024)
    else if (s ~ /KiB/) print int(n)
    else if (s ~ /kB/)  print int(n)
    else if (s ~ /MB/)  print int(n * 1000)
    else if (s ~ /GB/)  print int(n * 1000 * 1000)
    else if (s ~ /B/)   print int(n / 1024)
    else print 0
  }'
}

run_mode() { # $1 = hnsw | partitioned
  local mode="$1"
  info "=================  index: $mode  ================="
  $DC down -v >/dev/null 2>&1 || true

  SCHEMA_FILE="schema-${mode}.txt" $DC up -d zero >/dev/null
  wait_health "http://localhost:6080/health" "zero"

  # Phase 1: bulk load, sampling the bulk container's memory for the peak.
  info "bulk loading..."
  local t0 t1 peak_kb=0 cid rss_kb
  t0=$(date +%s)
  SCHEMA_FILE="schema-${mode}.txt" $DC up -d bulk >/dev/null
  cid=$($DC ps -q bulk)
  local used
  while [ "$(docker inspect -f '{{.State.Running}}' "$cid" 2>/dev/null)" = "true" ]; do
    # Best-effort peak sampling — never let it abort the run.
    used=$(docker stats --no-stream --format '{{.MemUsage}}' "$cid" 2>/dev/null | awk '{print $1}' || true)
    rss_kb=$(mem_to_kb "$used")
    if [ -n "$rss_kb" ] && [ "$rss_kb" -gt "$peak_kb" ] 2>/dev/null; then peak_kb=$rss_kb; fi
    sleep 2
  done
  t1=$(date +%s)
  local rc; rc=$(docker inspect -f '{{.State.ExitCode}}' "$cid")
  if [ "$rc" != "0" ]; then err "bulk failed (exit $rc)"; docker logs "$cid" | tail -30; $DC down -v; return 1; fi
  local wall=$((t1 - t0)) peak_mb=$((peak_kb / 1024))
  local pdir_mb; pdir_mb=$($DC run --rm --no-deps --entrypoint du alpha -sm /data/out/0/p 2>/dev/null | awk '{print $1}' || echo "")
  info "bulk done: ${wall}s, peak RSS ${peak_mb}MB, p-dir ${pdir_mb}MB"

  # Phase 2: serve the bulk output and measure recall.
  $DC up -d alpha >/dev/null
  wait_health "http://localhost:8080/health" "alpha"
  BULK_WALL_S="$wall" PEAK_RSS_MB="$peak_mb" PDIR_MB="$pdir_mb" uv run python bench.py "$mode"

  $DC down -v >/dev/null 2>&1 || true
}

IFS=',' read -ra MODES <<< "$INDEX_MODES"
for m in "${MODES[@]}"; do run_mode "$(echo "$m" | xargs)"; done

info "Done. Results appended to results/bulk_results.csv"
