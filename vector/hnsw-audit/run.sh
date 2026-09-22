#!/bin/bash
# HNSW unfindable-vector audit. For each efConstruction and each dgraph index
# mode (hnsw, partitioned): live-load N vectors, add the index (exercising the
# build path), then self-query a sample and classify found / rescued / absent.
# hnswlib runs the same classification in-process as the correct-HNSW baseline.
# One command, only Docker + uv required. Config lives in .env (from .env.example).
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
: "${N:=200000}"; : "${LIVE_THREADS:=8}"; export LIVE_THREADS
: "${INDEX_MODES:=hnsw,partitioned,hnswlib}"
: "${EF_CONSTRUCTION_LIST:=64,200}"
: "${DATASET:=gist-960-euclidean}"

# Preflight: only the dgraph modes need an image (hnswlib runs pure-python).
# Fail fast with an actionable message rather than a cryptic docker error.
case ",$INDEX_MODES," in
  *,hnsw,*|*,partitioned,*)
    IMAGE="${DGRAPH_IMAGE:-dgraph/dgraph}:${DGRAPH_VERSION}"
    if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
      if [ "$DGRAPH_VERSION" = "local" ]; then
        err "Image '$IMAGE' not found. The dgraph modes need a build whose loader"
        err "builds vector indexes (see README). Build one in your dgraph checkout"
        err "('make docker-image'), set DGRAPH_VERSION to a published tag, or run"
        err "INDEX_MODES=hnswlib to skip dgraph entirely."
        exit 1
      fi
      info "Image '$IMAGE' not present locally; Docker will pull it on first use."
    fi
    ;;
esac

info "Installing Python deps (uv sync)..."
uv sync --quiet

info "Preparing dataset $DATASET (N=$N) — downloads on first run, then materializes RDF..."
uv run python datasets.py "$DATASET" "$N"

wait_health() { # $1=url $2=label
  for _ in $(seq 1 120); do curl -sf "$1" >/dev/null 2>&1 && return 0; sleep 2; done
  err "$2 not healthy"; return 1
}

wait_indexing() {
  # Poll alpha health until the vector index build completes. health lists
  # "opIndexing" while a predicate is (re)indexing. We wait for it to appear
  # and then clear; if it never appears within the grace window the build was
  # instant (tiny data), so we proceed.
  local seen=0 i
  for i in $(seq 1 5400); do
    if curl -s http://localhost:8080/health 2>/dev/null | grep -q opIndexing; then
      seen=1
    else
      [ "$seen" = 1 ] && return 0
      [ "$i" -ge 10 ] && return 0
    fi
    sleep 2
  done
  return 0
}

audit_dgraph() { # $1 = hnsw | partitioned   $2 = efConstruction
  local mode="$1" efc="$2"
  info "=================  dgraph $mode  (efc=$efc)  ================="
  $DC down -v >/dev/null 2>&1 || true
  $DC up -d zero alpha >/dev/null
  wait_health "http://localhost:6080/health" "zero"
  wait_health "http://localhost:8080/health" "alpha"

  # Raw schema, then live-load the vectors (no index yet).
  curl -s localhost:8080/alter -d "$(uv run python config.py raw-schema)" >/dev/null
  info "live loading $N vectors..."
  $DC up --no-deps live >/dev/null 2>&1
  local rc; rc=$($DC ps -a --format '{{.Service}} {{.ExitCode}}' | awk '$1=="live"{print $2}')
  if [ "${rc:-1}" != "0" ]; then err "live load failed (exit ${rc:-?})"; $DC logs live | tail -20; $DC down -v; return 1; fi

  # Add the index — this triggers the build path under audit. Time it.
  info "building $mode index (efc=$efc)..."
  local t0 t1; t0=$(date +%s)
  curl -s --max-time 5400 localhost:8080/alter -d "$(uv run python config.py schema "$mode" "$efc")" >/dev/null
  wait_indexing
  t1=$(date +%s)
  info "$mode index built in $((t1 - t0))s"

  BUILD_S="$((t1 - t0))" uv run python audit.py "$mode" "$efc"
  $DC down -v >/dev/null 2>&1 || true
}

# Split requested modes into dgraph modes and the hnswlib baseline.
DGRAPH_MODES=(); RUN_HNSWLIB=0
IFS=',' read -ra MODES <<< "$INDEX_MODES"
for m in "${MODES[@]}"; do
  m=$(echo "$m" | xargs)
  case "$m" in
    hnsw|partitioned) DGRAPH_MODES+=("$m") ;;
    hnswlib) RUN_HNSWLIB=1 ;;
    "" ) ;;
    * ) err "unknown index mode '$m' (want hnsw, partitioned, hnswlib)"; exit 1 ;;
  esac
done

IFS=',' read -ra EFCS <<< "$EF_CONSTRUCTION_LIST"
for efc in "${EFCS[@]}"; do
  efc=$(echo "$efc" | xargs)
  for mode in "${DGRAPH_MODES[@]:-}"; do
    [ -n "$mode" ] && audit_dgraph "$mode" "$efc"
  done
done

if [ "$RUN_HNSWLIB" = "1" ]; then
  info "=================  hnswlib baseline (efc: $EF_CONSTRUCTION_LIST)  ================="
  uv run python hnswlib_ref.py
fi

info "Done. Results appended to results/audit_results.csv"
