#!/usr/bin/env bash
# bench.sh — end-to-end Dgraph k-shortest path correctness benchmark.
#
# ONE COMMAND from zero to results: downloads the dataset, builds each
# requested Dgraph branch, loads the data, sweeps maxfrontiersize values, and
# prints a correctness table comparing Dgraph's path-cost vector against the
# gonum Yen oracle.
#
# Usage:
#   ./scripts/bench.sh --dataset-url <url> [options] [branch ...]
#
# When no branches are given the default is "main".
#
# Examples:
#   # Benchmark main on roadCOL:
#   ./scripts/bench.sh --dataset-url https://example.com/roadCOL.gr.gz
#
#   # Compare two branches (dataset already on disk, skip re-download):
#   ./scripts/bench.sh --dataset-url <url> main pr-9599
#
#   # Run detached — memory-capped, survives SSH disconnect:
#   ./scripts/bench.sh --dataset-url <url> --detach main pr-9599
#
#   # Force a fresh bulk-load even though p/ is present:
#   ./scripts/bench.sh --dataset-url <url> --force-bulk main

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BENCH_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── defaults ──────────────────────────────────────────────────────────────────
DATASET_URL=""
DATASET_FORMAT="dimacs"
DATASET_NAME=""
SOURCE_VERTEX="1"
DGRAPH_REPO="${DGRAPH_REPO:-$HOME/dgraph}"
DGRAPH_REMOTE="https://github.com/dgraph-io/dgraph.git"
ALPHA_DIR="${ALPHA_DIR:-$HOME/db}"
RESULTS_DIR=""
FRONTIERS="100,1000,2000"
TARGETS="30"
NUMPATHS="2"
TIMEOUT="60s"
SEED="1"
BANDLO="0.0005"
BANDHI="0.004"
TOL="0.0001"
MEMORY_MAX="${MEMORY_MAX:-48G}"
MEMORY_SWAP_MAX="${MEMORY_SWAP_MAX:-1G}"
FORCE_BULK=0
DETACH=0
RUN_TAG=""
CAPTURE_PPROF="${CAPTURE_PPROF:-1}"
BRANCHES=()
ORIG_ARGS=("$@")

# ── helpers ───────────────────────────────────────────────────────────────────
ts()   { date +'%Y-%m-%d %H:%M:%S'; }
log()  { echo "[$(ts)] $*"; }
die()  { echo "[$(ts)] ERROR: $*" >&2; exit 1; }
warn() { echo "[$(ts)] WARN: $*" >&2; }

usage() {
cat <<'EOF'
Usage: ./scripts/bench.sh --dataset-url <url> [options] [branch ...]

Required (first run; skipped once the dataset is already on disk):
  --dataset-url   <url>       URL passed to cmd/prepare (download + extract).
                              Use a file:// URL for local archives.

Dataset options:
  --dataset-name  <name>      Directory name under datasets/ (default: URL basename).
  --dataset-format dimacs|csv Raw format for cmd/prepare (default: dimacs).
  --source-vertex <n>         SSSP source vertex ID in .properties file (default: 1).

Infrastructure:
  --dgraph-repo   <path>      Dgraph source tree; cloned if absent (default: $HOME/dgraph).
  --dgraph-remote <url>       Clone remote (default: github.com/dgraph-io/dgraph).
  --alpha-dir     <path>      Alpha workspace for p/t/w/zw (default: $HOME/db).
  --results-dir   <path>      Output root (default: <repo>/results).

Sweep tuning:
  --frontiers     <list>      Comma-separated maxfrontiersize values (default: 100,1000,2000).
  --targets       <n>         Banded target pairs per run (default: 30).
  --numpaths      <n>         k for k-shortest queries (default: 2).
  --timeout       <dur>       Per-query wall-clock limit (default: 60s).

Memory (--detach only):
  --memory-max    <size>      cgroup hard cap (default: 48G).
  --memory-swap-max <size>    cgroup swap cap (default: 1G).

Run control:
  --force-bulk                Re-run bulk load even when p/ is present.
  --detach                    Launch in a memory-capped systemd transient unit
                              (survives SSH disconnect); prints watch commands and exits.
  -h, --help                  Print this message.
EOF
}

# ── arg parsing ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dataset-url)      DATASET_URL="$2";     shift 2 ;;
        --dataset-format)   DATASET_FORMAT="$2";  shift 2 ;;
        --dataset-name)     DATASET_NAME="$2";    shift 2 ;;
        --source-vertex)    SOURCE_VERTEX="$2";   shift 2 ;;
        --dgraph-repo)      DGRAPH_REPO="$2";     shift 2 ;;
        --dgraph-remote)    DGRAPH_REMOTE="$2";   shift 2 ;;
        --alpha-dir)        ALPHA_DIR="$2";       shift 2 ;;
        --results-dir)      RESULTS_DIR="$2";     shift 2 ;;
        --frontiers)        FRONTIERS="$2";       shift 2 ;;
        --targets)          TARGETS="$2";         shift 2 ;;
        --numpaths)         NUMPATHS="$2";        shift 2 ;;
        --timeout)          TIMEOUT="$2";         shift 2 ;;
        --seed)             SEED="$2";            shift 2 ;;
        --band-lo)          BANDLO="$2";          shift 2 ;;
        --band-hi)          BANDHI="$2";          shift 2 ;;
        --tol)              TOL="$2";             shift 2 ;;
        --memory-max)       MEMORY_MAX="$2";      shift 2 ;;
        --memory-swap-max)  MEMORY_SWAP_MAX="$2"; shift 2 ;;
        --force-bulk)       FORCE_BULK=1;         shift ;;
        --detach)           DETACH=1;             shift ;;
        --run-tag)          RUN_TAG="$2";         shift 2 ;;  # internal: set by --detach re-launch
        -h|--help)          usage; exit 0 ;;
        --*)                die "unknown option: $1  (see --help)" ;;
        *)                  BRANCHES+=("$1");      shift ;;
    esac
done

[[ ${#BRANCHES[@]} -eq 0 ]] && BRANCHES=("main")

# Derive dataset name from URL when not given explicitly.
if [[ -z "$DATASET_NAME" && -n "$DATASET_URL" ]]; then
    DATASET_NAME="$(basename "$DATASET_URL")"
    DATASET_NAME="${DATASET_NAME%%.*}"
fi

RESULTS_DIR="${RESULTS_DIR:-$BENCH_DIR/results}"
KS_DIR="$RESULTS_DIR/kshortest"
LOG_DIR="$RESULTS_DIR/logs"
ZERO_DIR="${ZERO_DIR:-$ALPHA_DIR/zero-setup}"
ALPHA_DIR_PREFIX_ALLOW="$(dirname "$ALPHA_DIR")/"

# Endpoints (overridable for non-localhost setups).
ALPHA_HTTP_URL="${ALPHA_HTTP_URL:-http://localhost:8080}"
ALPHA_HEALTH_URL="$ALPHA_HTTP_URL/health"
ALPHA_GRPC="${ALPHA_GRPC:-localhost:9080}"
ZERO_STATE_URL="${ZERO_STATE_URL:-http://localhost:6080/state}"
ZERO_GRPC_ADDR="${ZERO_GRPC_ADDR:-localhost:5080}"
ALPHA_HEALTH_TIMEOUT_SEC="${ALPHA_HEALTH_TIMEOUT_SEC:-300}"

# alpha.sh uses DATASETS array in guard_rm_target to protect bulk p/ from rm.
DATASETS=("${DATASET_NAME:-_unset_}")
source "$SCRIPT_DIR/lib/alpha.sh"

# ── systemd availability check (used by detach and ensure_zero) ───────────────
HAVE_SYSTEMD=0
if [[ "${USE_SYSTEMD:-1}" == "1" ]] \
        && command -v systemd-run >/dev/null 2>&1 \
        && sudo -n true 2>/dev/null; then
    HAVE_SYSTEMD=1
fi

# ── detach: re-launch self inside a memory-capped systemd transient unit ──────
if [[ $DETACH -eq 1 ]]; then
    [[ -n "$DATASET_NAME" ]] \
        || die "--dataset-url or --dataset-name is required when using --detach"

    LABEL=$(printf '%s' "${BRANCHES[*]}" | tr ' ' '-')
    RUN_TAG="$(date +%Y%m%d-%H%M%S)"
    mkdir -p "$RESULTS_DIR"
    OUT="$RESULTS_DIR/sweep-$LABEL-$RUN_TAG.out"
    : > "$OUT"
    ln -sfn "$OUT" "$RESULTS_DIR/sweep-$LABEL-latest.out"

    # Stop any stray bench/alpha from a previous run.
    # Zero is shared infra holding uid-lease state -- never touched here.
    sudo systemctl stop 'dgraph-bench-*' 2>/dev/null || true
    pkill -f 'cmd/bench'    2>/dev/null || true
    pkill -f 'dgraph alpha' 2>/dev/null || true
    sleep 2

    # Forward all original args minus --detach; append internal --run-tag so
    # the unit's JSON artifacts share the same tag as the .out file.
    fwd_args=()
    for arg in "${ORIG_ARGS[@]}"; do
        [[ "$arg" == "--detach" ]] || fwd_args+=("$arg")
    done
    fwd_args+=(--run-tag "$RUN_TAG")

    SCRIPT_PATH="$(realpath "${BASH_SOURCE[0]}")"

    if (( HAVE_SYSTEMD )); then
        UNIT="dgraph-bench-$LABEL"
        sudo systemctl reset-failed "$UNIT" 2>/dev/null || true
        sudo systemd-run --unit="$UNIT" --collect \
            -p MemoryMax="$MEMORY_MAX" \
            -p MemorySwapMax="$MEMORY_SWAP_MAX" \
            -p OOMPolicy=continue \
            -p "User=$(id -un)" \
            -p "WorkingDirectory=$BENCH_DIR" \
            -p "StandardOutput=append:$OUT" \
            -p "StandardError=append:$OUT" \
            --setenv=HOME="$HOME" \
            --setenv=PATH="$PATH" \
            "$SCRIPT_PATH" "${fwd_args[@]}"
        echo "[bench] launched as unit $UNIT (MemoryMax=$MEMORY_MAX MemorySwapMax=$MEMORY_SWAP_MAX)"
        echo "[bench] watch:  journalctl -u $UNIT -f"
        echo "[bench] status: systemctl status $UNIT"
    else
        warn "systemd-run / passwordless sudo unavailable -- using nohup (no memory cap)"
        warn "a runaway query can exhaust RAM; consider systemd or running on a cgroup-enabled VM"
        nohup "$SCRIPT_PATH" "${fwd_args[@]}" >> "$OUT" 2>&1 &
        disown
        echo "[bench] launched (pid $!)"
    fi
    echo "[bench] run tag: $RUN_TAG"
    echo "[bench] watch:   tail -f $OUT"
    echo "[bench]          tail -f $RESULTS_DIR/sweep-$LABEL-latest.out"
    echo "[bench] results: $KS_DIR/<branch>-${DATASET_NAME}-${RUN_TAG}.json (written per branch)"
    exit 0
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Synchronous run
# ═══════════════════════════════════════════════════════════════════════════════
RUN_TAG="${RUN_TAG:-$(date +%Y%m%d-%H%M%S)}"
LABEL=$(printf '%s' "${BRANCHES[*]}" | tr ' ' '-')
mkdir -p "$KS_DIR" "$LOG_DIR"
MASTER_LOG="$LOG_DIR/master-$RUN_TAG.log"
exec > >(tee -a "$MASTER_LOG") 2>&1

log "================================================================="
log " bench.sh — k-shortest path correctness benchmark"
log "================================================================="
log "  run tag:      $RUN_TAG"
log "  branches:     ${BRANCHES[*]}"
log "  dataset:      ${DATASET_NAME:-(not yet known)}  format=$DATASET_FORMAT"
log "  frontiers:    $FRONTIERS"
log "  targets:      $TARGETS  numpaths=$NUMPATHS  timeout=$TIMEOUT"
log "  dgraph repo:  $DGRAPH_REPO"
log "  alpha dir:    $ALPHA_DIR"
log "  results dir:  $RESULTS_DIR"
log "================================================================="

trap 'stop_memlog; alpha_cleanup_on_exit' EXIT

# ── phase 1: preflight ────────────────────────────────────────────────────────
log ""
log "=== [1/5] preflight ==="
preflight_memory
for bin in dgraph go git make curl jq awk; do
    command -v "$bin" >/dev/null || die "$bin not found on PATH"
done
[[ -d "$BENCH_DIR" ]] || die "BENCH_DIR not found: $BENCH_DIR"
( cd "$BENCH_DIR" && go build ./... ) 2>&1 \
    | tee "$LOG_DIR/build-bench-$RUN_TAG.log" \
    || die "bench tools failed to compile -- see $LOG_DIR/build-bench-$RUN_TAG.log"
log "  tools OK"

# ── phase 2: dgraph repo ──────────────────────────────────────────────────────
log ""
log "=== [2/5] dgraph repo ($DGRAPH_REPO) ==="
if [[ ! -d "$DGRAPH_REPO/.git" ]]; then
    log "  cloning $DGRAPH_REMOTE -> $DGRAPH_REPO (this may take a few minutes)"
    git clone "$DGRAPH_REMOTE" "$DGRAPH_REPO" 2>&1 \
        | tee "$LOG_DIR/clone-$RUN_TAG.log" \
        || die "git clone failed -- see $LOG_DIR/clone-$RUN_TAG.log"
fi
( cd "$DGRAPH_REPO"
  log "  fetching origin..."
  git fetch origin 2>&1 \
      | tee "$LOG_DIR/fetch-$RUN_TAG.log" \
      | tail -5 || warn "git fetch failed (offline?)"
  git diff --quiet && git diff --cached --quiet \
      || die "dgraph working tree is dirty -- commit or stash before benchmarking"
  for br in "${BRANCHES[@]}"; do
      if ! git rev-parse --verify --quiet "$br" >/dev/null 2>&1; then
          git branch "$br" "origin/$br" 2>/dev/null \
              || die "branch '$br' not found locally or on origin (check spelling or run git fetch)"
          log "  created local branch $br from origin/$br"
      else
          log "  branch $br present"
      fi
  done )
log "  repo ready"

# ── phase 3: dataset ──────────────────────────────────────────────────────────
log ""
log "=== [3/5] dataset ($DATASET_NAME) ==="
[[ -n "$DATASET_NAME" ]] || die "--dataset-url is required (cannot infer dataset name)"
DATASETS=("$DATASET_NAME")
ds_dir="$BENCH_DIR/datasets/$DATASET_NAME"

if [[ ! -f "$ds_dir/$DATASET_NAME.properties" ]]; then
    [[ -n "$DATASET_URL" ]] \
        || die "--dataset-url is required: dataset not found at $ds_dir"
    log "  running cmd/prepare (download + extract -- may take a few minutes)"
    log "  url=$DATASET_URL  format=$DATASET_FORMAT  name=$DATASET_NAME  source=$SOURCE_VERTEX"
    ( cd "$BENCH_DIR" && go run ./cmd/prepare \
        -format  "$DATASET_FORMAT" \
        -name    "$DATASET_NAME" \
        -url     "$DATASET_URL" \
        -source  "$SOURCE_VERTEX" \
    ) 2>&1 | tee "$LOG_DIR/prepare-$DATASET_NAME-$RUN_TAG.log" \
        || die "cmd/prepare failed -- see $LOG_DIR/prepare-$DATASET_NAME-$RUN_TAG.log"
    [[ -f "$ds_dir/$DATASET_NAME.properties" ]] \
        || die "cmd/prepare completed but $ds_dir/$DATASET_NAME.properties not found"
else
    log "  already extracted at $ds_dir (skipping prepare)"
fi

rdf="$ds_dir/dgraph/graph.rdf.gz"
schema="$ds_dir/dgraph/graph.schema"
if [[ ! -f "$rdf" || ! -f "$schema" ]]; then
    log "  running cmd/convert (raw -> RDF + schema)"
    ( cd "$BENCH_DIR" && go run ./cmd/convert -dataset "$ds_dir" ) \
        2>&1 | tee "$LOG_DIR/convert-$DATASET_NAME-$RUN_TAG.log" \
        || die "cmd/convert failed -- see $LOG_DIR/convert-$DATASET_NAME-$RUN_TAG.log"
    [[ -f "$rdf" && -f "$schema" ]] \
        || die "cmd/convert did not produce $rdf and/or $schema"
else
    log "  RDF + schema already present (skipping convert)"
fi
log "  dataset ready: $ds_dir"

# ── phase 4: zero + bulk load ─────────────────────────────────────────────────
log ""
log "=== [4/5] zero + bulk load ==="

zero_up() { curl -s -m 3 "$ZERO_STATE_URL" >/dev/null 2>&1; }

ensure_zero() {
    log "  ensuring zero is running..."
    if (( HAVE_SYSTEMD )); then
        local dgraph_bin
        dgraph_bin=$(command -v dgraph) || die "dgraph not on PATH"
        mkdir -p "$ZERO_DIR"
        local unit_file=/etc/systemd/system/dgraph-zero.service
        # Write the unit file only when the content has changed (idempotent).
        local desired
        desired="[Unit]
Description=Dgraph Zero (bench infra — do not stop between branch runs)
After=network.target

[Service]
Type=simple
User=$(id -un)
WorkingDirectory=$ZERO_DIR
ExecStart=$dgraph_bin zero --my=$ZERO_GRPC_ADDR --replicas=1
Restart=on-failure
RestartSec=5
MemoryMax=4G
OOMScoreAdjust=-500
StandardOutput=append:$ZERO_DIR/zero.log
StandardError=append:$ZERO_DIR/zero.log

[Install]
WantedBy=multi-user.target"
        if [[ ! -f "$unit_file" ]] \
                || ! diff -q <(printf '%s\n' "$desired") "$unit_file" >/dev/null 2>&1; then
            printf '%s\n' "$desired" | sudo tee "$unit_file" >/dev/null
            sudo systemctl daemon-reload
            log "  wrote $unit_file"
        fi
        sudo systemctl enable dgraph-zero >/dev/null 2>&1 || true
        if zero_up; then
            if systemctl is-active --quiet dgraph-zero; then
                log "  zero running (dgraph-zero.service) -- leaving it alone"
            else
                log "  zero running (legacy process) -- leaving it alone"
                log "  (dgraph-zero.service is enabled and takes over on next reboot)"
            fi
        else
            pkill -f 'dgraph zero' 2>/dev/null || true
            sleep 1
            sudo systemctl restart dgraph-zero
            for _ in $(seq 1 15); do zero_up && break; sleep 2; done
            zero_up || die "zero failed to start -- journalctl -u dgraph-zero -n 50"
            log "  zero started via dgraph-zero.service"
        fi
    else
        # No systemd: start via nohup and leave it running. Zero is shared infra;
        # it is NOT stopped on exit -- only alpha is cleaned up in the EXIT trap.
        if zero_up; then
            log "  zero already running"
        else
            log "  starting zero (nohup) in $ZERO_DIR"
            pkill -f 'dgraph zero' 2>/dev/null || true
            sleep 1
            mkdir -p "$ZERO_DIR"
            ( cd "$ZERO_DIR" && nohup dgraph zero --my="$ZERO_GRPC_ADDR" --replicas=1 \
                > zero.log 2>&1 & )
            sleep 6
            zero_up || die "zero failed to start -- see $ZERO_DIR/zero.log"
            log "  zero started (nohup -- install systemd for reboot-proof operation)"
        fi
    fi
}

ensure_zero

bp=$(bulk_p_for "$DATASET_NAME")
if [[ -d "$bp" && $FORCE_BULK -eq 0 ]]; then
    log "  bulk p/ present: $bp ($(size_of "$bp"))"
    log "  skipping bulk load (pass --force-bulk to redo)"
else
    if [[ $FORCE_BULK -eq 1 && -d "$bp" ]]; then
        log "  --force-bulk: removing existing $bp"
        rm -rf "$bp"
    fi
    log "  running dgraph bulk (this takes several minutes)..."
    ( cd "$ds_dir/dgraph" && dgraph bulk \
        -f   "$rdf" \
        -s   "$schema" \
        --zero "$ZERO_GRPC_ADDR" \
        --out  bulk-out \
    ) 2>&1 | tee "$LOG_DIR/bulk-$DATASET_NAME-$RUN_TAG.log" \
        || die "dgraph bulk failed -- see $LOG_DIR/bulk-$DATASET_NAME-$RUN_TAG.log"
    [[ -d "$bp" ]] || die "bulk completed but expected p/ not found at $bp"
    log "  bulk done: $bp ($(size_of "$bp"))"
fi
require_zero

# ── phase 5: sweep ────────────────────────────────────────────────────────────
log ""
log "=== [5/5] sweep (${#BRANCHES[@]} branch(es) × frontiers: $FRONTIERS) ==="
start_memlog "$LOG_DIR/memlog-$RUN_TAG.log"
stop_alpha

first=1
pprof_pid=""
for branch in "${BRANCHES[@]}"; do
    log ""
    log "─── branch: $branch ───"

    if ! ( cd "$DGRAPH_REPO"
           git checkout "$branch" >/dev/null 2>&1
           make install ) 2>&1 | tee "$LOG_DIR/build-$branch-$RUN_TAG.log"; then
        warn "[$branch] build failed -- skipping (see $LOG_DIR/build-$branch-$RUN_TAG.log)"
        continue
    fi

    # Verify the installed binary is actually this branch's commit.
    # Catches a stale PATH or a make install that silently used a cached binary.
    want_sha=$(cd "$DGRAPH_REPO" && git rev-parse --short=9 "$branch")
    bin_sha=$(dgraph version 2>/dev/null \
        | awk -F: '/Commit SHA-1/{gsub(/[[:space:]]/,"",$2); print $2}')
    [[ -n "$bin_sha" ]] \
        || die "[$branch] cannot parse 'Commit SHA-1' from dgraph version (PATH/install problem)"
    if [[ "$bin_sha" != "$want_sha"* && "$want_sha" != "$bin_sha"* ]]; then
        die "[$branch] binary mismatch: branch=$want_sha  binary=$bin_sha  (stale PATH?)"
    fi
    label="${branch}@${bin_sha}"
    log "[$branch] binary verified: $label"

    alpha_log="$LOG_DIR/alpha-$branch-$RUN_TAG.log"
    stop_alpha
    reset_data "$bp"
    start_alpha "$alpha_log"

    if ! wait_alpha; then
        warn "[$branch] alpha unhealthy after ${ALPHA_HEALTH_TIMEOUT_SEC}s -- skipping"
        tail_log "[$branch] alpha" "$alpha_log"
        stop_alpha
        continue
    fi

    # /health goes green before bulk tablets are queryable (alpha must load
    # postings + register tablets with zero). Poll until data actually serves.
    log "[$branch] waiting for bulk data to be served..."
    data_ok=0
    for _ in $(seq 1 90); do
        n=$(curl -s -m 5 -H 'Content-Type: application/dql' "$ALPHA_HTTP_URL/query" \
              -d '{ q(func: has(graphalytics_id)) { count(uid) } }' 2>/dev/null \
              | jq -r '.data.q[0].count // 0' 2>/dev/null)
        if [[ "${n:-0}" =~ ^[0-9]+$ ]] && (( n > 0 )); then
            data_ok=1; log "[$branch] data serving: $n nodes"; break
        fi
        sleep 2
    done
    if (( data_ok == 0 )); then
        warn "[$branch] bulk data not served within 180s -- skipping"
        stop_alpha
        continue
    fi

    # UIDs are stable across branches (same dataset, same bulk load). Fetch
    # the uid map only on the first branch; all others reuse the cache.
    refresh=""
    if (( first == 1 )); then refresh="-refresh-uidmap"; first=0; fi

    # CPU profile + goroutine dump taken ~40s into the run: on a binary that
    # exercises eviction, pprof -top shows removeMax/pq.Pop/expandOut.
    if [[ "${CAPTURE_PPROF:-1}" == "1" ]]; then
        ( sleep 40
          curl -s "${ALPHA_HTTP_URL}/debug/pprof/profile?seconds=30" \
              -o "$KS_DIR/pprof-cpu-$branch-$RUN_TAG.prof" 2>/dev/null
          curl -s "${ALPHA_HTTP_URL}/debug/pprof/goroutine?debug=2" \
              -o "$KS_DIR/pprof-goroutine-$branch-$RUN_TAG.txt" 2>/dev/null
        ) &
        pprof_pid=$!
    fi

    out="$KS_DIR/${branch}-${DATASET_NAME}-${RUN_TAG}.json"
    log "[$branch] bench -> $out"
    if ! ( cd "$BENCH_DIR" && go run ./cmd/bench \
              -mode      kshortest \
              -dataset   "$ds_dir" \
              -alpha     "$ALPHA_GRPC" \
              -numpaths  "$NUMPATHS" \
              -targets   "$TARGETS" \
              -frontiers "$FRONTIERS" \
              -band-lo   "$BANDLO"   -band-hi "$BANDHI" \
              -tol       "$TOL"      -timeout "$TIMEOUT"  -seed "$SEED" \
              -label     "$label" \
              $refresh \
              -out       "$out" \
    ) 2>&1 | tee "$LOG_DIR/bench-$branch-$RUN_TAG.log"; then
        warn "[$branch] bench failed -- see $LOG_DIR/bench-$branch-$RUN_TAG.log"
        apid=$(cat /tmp/dgraph-alpha.pid 2>/dev/null || true)
        if [[ -z "$apid" ]] || ! kill -0 "$apid" 2>/dev/null; then
            warn "[$branch] alpha is no longer running (OOM-killed or crashed mid-bench)"
            tail_log "[$branch] alpha" "$alpha_log"
        fi
    fi

    [[ -n "$pprof_pid" ]] && { wait "$pprof_pid" 2>/dev/null || true; pprof_pid=""; }
    stop_alpha
done

# ── results ───────────────────────────────────────────────────────────────────
log ""
log "================================================================="
log " RESULTS: correct-of-returned%  (r=returned  t=timed-out)"
log "================================================================="
printf '%-12s' "frontier"
for b in "${BRANCHES[@]}"; do printf ' %-22s' "$b"; done
echo
printf '%-12s' "------------"
for b in "${BRANCHES[@]}"; do printf ' %-22s' "----------------------"; done
echo
for fr in ${FRONTIERS//,/ }; do
    flabel=$fr; [[ "$fr" == "0" ]] && flabel="unlimited"
    printf '%-12s' "$flabel"
    for b in "${BRANCHES[@]}"; do
        f="$KS_DIR/${b}-${DATASET_NAME}-${RUN_TAG}.json"
        if [[ -f "$f" ]]; then
            cell=$(jq -r --argjson fr "$fr" \
                '.frontiers[]? | select(.max_frontier==$fr)
                 | "\(.correct_of_returned_pct|floor)%(r\(.returned) t\(.timeouts))"' \
                "$f" 2>/dev/null)
            printf ' %-22s' "${cell:-NA}"
        else
            printf ' %-22s' "norun"
        fi
    done
    echo
done

log ""
log "artifacts:  $KS_DIR/*-${DATASET_NAME}-${RUN_TAG}.json"
log "pprof:      $KS_DIR/pprof-cpu-<branch>-${RUN_TAG}.prof"
log "            go tool pprof -top <file> | grep -iE 'removeMax|pq.Pop|expandOut'"
log "master log: $MASTER_LOG"
