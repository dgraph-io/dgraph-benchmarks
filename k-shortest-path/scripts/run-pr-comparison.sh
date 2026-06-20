#!/usr/bin/env bash
# run-pr-comparison.sh -- the production sweep.
#
# Runs `cmd/bench -mode correctness` per (branch, dataset, run) cell:
#   BRANCHES        x  DATASETS  x  RUNS_PER_BRANCH = N JSON files in
#   $RESULTS_DIR/<branch>/<dataset>-run<N>.json
#
# Per branch (once): git checkout + make install in $DGRAPH_REPO.
# Per cell (every time):
#   stop alpha
#   wipe $ALPHA_DIR/{p,t,w,zw}
#   cp -r <dataset's bulk-loaded p/> -> $ALPHA_DIR/p
#   start alpha, wait for /health
#   go run ./cmd/bench ... -out <cell-json>
#
# SAFETY:
#   rm operations gated by guard_rm_target: must be absolute, under
#   ALPHA_DIR_PREFIX_ALLOW, not $HOME, not /, not any dataset's bulk p/.
#
# INPUTS this script honors (set in env or override per-invocation):
#
#   Paths (must match what setup.sh produced on this machine):
#     BENCH_DIR=/srv/shortest-path-bench
#     DGRAPH_REPO=/srv/dgraph
#     ALPHA_DIR=/srv/db
#     ALPHA_DIR_PREFIX_ALLOW=/srv/
#
#   What to compare:
#     BRANCHES_OVERRIDE="main pr-9576 pr-9599 pr-9607 pr-9678"
#     DATASETS_OVERRIDE="kgs datagen-7_5-fb"
#     RUNS_OVERRIDE=4
#
#   Per-dataset max-frontier (from calibrate.sh; falls back to MAXFRONTIER global):
#     MAXFRONTIER_KGS=200
#     MAXFRONTIER_DATAGEN_7_5_FB=10000
#     MAXFRONTIER=1000        # fallback if no per-dataset value set
#
#   Bench knobs:
#     TARGETS=100
#     NUMPATHS=2              # the bug-triggering value -- don't lower without reason
#     CONCURRENCY=8
#     SEED=1
#     TIMEOUT=5m              # 60s gives all-errors on builds that still hang; do NOT lower
#
#   Probe gating:
#     PROBE_RESULTS=$RESULTS_DIR/probe/probe-results.json
#     RUN_HUNG=0              # set =1 to ignore probe's HANG verdict and run anyway
#
# This script does NOT run setup, probe, or calibrate. Run those first:
#   ./scripts/setup.sh
#   ./scripts/probe.sh
#   ./scripts/calibrate.sh    # source the exports it emits into your env
#   ./scripts/run-pr-comparison.sh

set -euo pipefail

# ============================================================================
# CONFIG
# ============================================================================
BENCH_DIR="${BENCH_DIR:-/Users/shiva/workspace/shortest-path-bench}"
DGRAPH_REPO="${DGRAPH_REPO:-/Users/shiva/workspace/dgraph-scratch/dgraph}"
ALPHA_DIR="${ALPHA_DIR:-/Users/shiva/workspace/db}"
ALPHA_DIR_PREFIX_ALLOW="${ALPHA_DIR_PREFIX_ALLOW:-/Users/shiva/workspace/}"

BRANCHES_STR="${BRANCHES_OVERRIDE:-main pr-9576 pr-9599 pr-9607 pr-9678}"
read -ra BRANCHES <<< "$BRANCHES_STR"

DATASETS_STR="${DATASETS_OVERRIDE:-kgs datagen-7_5-fb}"
read -ra DATASETS <<< "$DATASETS_STR"

RUNS_PER_BRANCH="${RUNS_OVERRIDE:-4}"

TARGETS="${TARGETS:-100}"
NUMPATHS="${NUMPATHS:-2}"
CONCURRENCY="${CONCURRENCY:-8}"
SEED="${SEED:-1}"
TIMEOUT="${TIMEOUT:-5m}"
# Global fallback. Per-dataset MAXFRONTIER_<DS> takes precedence (see maxfrontier_for).
MAXFRONTIER="${MAXFRONTIER:-1000}"

ALPHA_HTTP_URL="${ALPHA_HTTP_URL:-http://localhost:8080}"
ALPHA_HEALTH_URL="$ALPHA_HTTP_URL/health"
ALPHA_GRPC="${ALPHA_GRPC:-localhost:9080}"
ZERO_STATE_URL="${ZERO_STATE_URL:-http://localhost:6080/state}"
ALPHA_HEALTH_TIMEOUT_SEC="${ALPHA_HEALTH_TIMEOUT_SEC:-300}"

RESULTS_DIR="${RESULTS_DIR:-$BENCH_DIR/results}"
LOG_DIR="$RESULTS_DIR/logs"
PROBE_RESULTS="${PROBE_RESULTS:-$RESULTS_DIR/probe/probe-results.json}"
RUN_HUNG="${RUN_HUNG:-0}"

# Shared Alpha lifecycle + safety helpers (stop_alpha, wait_alpha, reset_data,
# start_alpha, guard_rm_target, bulk_p_for, require_zero, log/die/warn, etc.)
source "$(dirname "${BASH_SOURCE[0]}")/lib/alpha.sh"
trap alpha_cleanup_on_exit EXIT

# ============================================================================
# run-pr-comparison-specific helpers
# ============================================================================
maxfrontier_for() {
    local ds="$1"
    local upper
    upper=$(printf '%s' "$ds" | tr '[:lower:]-.' '[:upper:]__')
    local var="MAXFRONTIER_$upper"
    if [[ -n "${!var:-}" ]]; then
        echo "${!var}"
    else
        echo "$MAXFRONTIER"
    fi
}

# Returns 0 (true) if the (branch, dataset) cell is eligible to run, 1 otherwise.
# Eligibility = probe results either missing OR show status=PASS for this cell,
# OR RUN_HUNG=1 (bypass).
probe_eligible() {
    local branch="$1"
    local ds="$2"
    if (( RUN_HUNG == 1 )); then return 0; fi
    if [[ ! -f "$PROBE_RESULTS" ]]; then return 0; fi
    local st
    st=$(jq -r --arg br "$branch" --arg ds "$ds" \
        '.cells[]? | select(.branch==$br and .dataset==$ds) | .status' \
        "$PROBE_RESULTS" 2>/dev/null || echo "")
    if [[ -z "$st" ]]; then return 0; fi   # no entry -> assume runnable
    [[ "$st" == "PASS" ]]
}

# ============================================================================
# Pre-flight (no destructive ops)
# ============================================================================
mkdir -p "$RESULTS_DIR" "$LOG_DIR"

log "================================================================="
log " run-pr-comparison.sh -- full sweep"
log "================================================================="

log "[pre-flight] checking host binaries..."
for bin in dgraph go git make curl jq awk; do
    command -v "$bin" >/dev/null || die "$bin not on PATH"
done

log "[pre-flight] checking directories..."
[[ -d "$BENCH_DIR" ]]   || die "BENCH_DIR not found: $BENCH_DIR"
[[ -d "$DGRAPH_REPO" ]] || die "DGRAPH_REPO not found: $DGRAPH_REPO"
[[ -d "$ALPHA_DIR" ]]   || die "ALPHA_DIR not found: $ALPHA_DIR"
[[ "$ALPHA_DIR" == "$ALPHA_DIR_PREFIX_ALLOW"* ]] \
    || die "ALPHA_DIR ($ALPHA_DIR) not under allowed prefix $ALPHA_DIR_PREFIX_ALLOW"
[[ "$ALPHA_DIR" != "/" ]]    || die "ALPHA_DIR is /"
[[ "$ALPHA_DIR" != "$HOME" ]] || die "ALPHA_DIR equals HOME"

log "[pre-flight] checking branches in dgraph repo..."
( cd "$DGRAPH_REPO"
  if ! git diff --quiet || ! git diff --cached --quiet; then
      die "dgraph working tree is dirty -- commit/stash before sweeping"
  fi
  for br in "${BRANCHES[@]}"; do
      git rev-parse --verify --quiet "$br" >/dev/null \
          || die "branch '$br' not found locally in $DGRAPH_REPO"
  done )

log "[pre-flight] checking datasets + bulk p/ caches..."
for ds in "${DATASETS[@]}"; do
    ds_dir="$BENCH_DIR/datasets/$ds"
    [[ -d "$ds_dir" ]]                       || die "dataset dir missing: $ds_dir"
    [[ -f "$ds_dir/$ds.properties" ]]        || die "$ds_dir/$ds.properties missing"
    bp=$(bulk_p_for "$ds")
    [[ -d "$bp" ]]                           || die "bulk-loaded p/ missing for $ds at $bp -- did setup.sh run?"
    mf=$(maxfrontier_for "$ds")
    log "  $ds: bulk_p=$bp ($(size_of "$bp"))  MAXFRONTIER=$mf"
done

log "[pre-flight] checking bench tool compiles..."
( cd "$BENCH_DIR" && go build ./... ) || die "bench failed to compile in $BENCH_DIR"

require_zero

if [[ -f "$PROBE_RESULTS" ]]; then
    log "[pre-flight] probe results present at $PROBE_RESULTS"
    if (( RUN_HUNG == 1 )); then
        log "             RUN_HUNG=1 -- will ignore probe verdict and run every cell"
    else
        log "             cells with status != PASS will be SKIPPED (set RUN_HUNG=1 to override)"
    fi
else
    log "[pre-flight] no probe results at $PROBE_RESULTS"
    log "             every (branch, dataset) cell will run; probe.sh first is recommended"
fi

MASTER_LOG="$LOG_DIR/sweep-$(date +%Y%m%d-%H%M%S).log"
log "[pre-flight] passed. all output also tee'd to $MASTER_LOG"
exec > >(tee -a "$MASTER_LOG") 2>&1

log ""
log "config:"
log "  BENCH_DIR              = $BENCH_DIR"
log "  DGRAPH_REPO            = $DGRAPH_REPO"
log "  ALPHA_DIR              = $ALPHA_DIR"
log "  ALPHA_DIR_PREFIX_ALLOW = $ALPHA_DIR_PREFIX_ALLOW"
log "  branches               = ${BRANCHES[*]}"
log "  datasets               = ${DATASETS[*]}"
log "  runs/branch            = $RUNS_PER_BRANCH"
log "  targets/run            = $TARGETS"
log "  numpaths               = $NUMPATHS"
log "  concurrency            = $CONCURRENCY"
log "  seed                   = $SEED"
log "  per-query timeout      = $TIMEOUT"
log "  global maxfrontier     = $MAXFRONTIER (per-dataset override via MAXFRONTIER_<DS>)"

stop_alpha

# ============================================================================
# Main loop
# ============================================================================
SCRIPT_START=$(date +%s)

# Counts for summary.
declare -A CELLS_RAN
declare -A CELLS_SKIPPED

for branch in "${BRANCHES[@]}"; do
    BRANCH_START=$(date +%s)
    log ""
    log "================================================================="
    log " BRANCH: $branch"
    log "================================================================="

    log "[$branch] git checkout + make install"
    if ! ( cd "$DGRAPH_REPO"
           git checkout "$branch" >/dev/null 2>&1
           make install ) 2>&1 | tee "$LOG_DIR/build-$branch.log" ; then
        warn "[$branch] BUILD FAILED -- skipping all cells for this branch (see $LOG_DIR/build-$branch.log)"
        for ds in "${DATASETS[@]}"; do
            CELLS_SKIPPED["$branch:$ds"]="build_fail"
        done
        continue
    fi

    bin_branch=$(dgraph version 2>/dev/null | awk '/^Branch/ {print $3; exit}' || true)
    if [[ -z "$bin_branch" ]]; then
        warn "[$branch] could not parse Branch from 'dgraph version'"
    elif [[ "$bin_branch" != "$branch" ]]; then
        die "[$branch] binary reports Branch='$bin_branch' (expected '$branch'). PATH issue?"
    else
        log "[$branch] binary branch confirmed: $bin_branch"
    fi

    for ds in "${DATASETS[@]}"; do
        cell="$branch:$ds"
        cap=$(maxfrontier_for "$ds")
        bp=$(bulk_p_for "$ds")

        if ! probe_eligible "$branch" "$ds"; then
            log "[$cell] SKIPPED -- probe says non-PASS for this cell (set RUN_HUNG=1 to override)"
            CELLS_SKIPPED["$cell"]="probe_hang"
            continue
        fi

        log ""
        log "----- $cell  maxfrontier=$cap -----"

        branch_results_dir="$RESULTS_DIR/$branch"
        mkdir -p "$branch_results_dir"

        for run in $(seq 1 "$RUNS_PER_BRANCH"); do
            log "[$cell] run $run/$RUNS_PER_BRANCH"

            stop_alpha
            reset_data "$bp"
            start_alpha "$LOG_DIR/alpha-$branch-$ds-run${run}.log"
            if ! wait_alpha; then
                warn "[$cell run $run] alpha didn't become healthy in ${ALPHA_HEALTH_TIMEOUT_SEC}s; skipping remaining runs of this cell"
                CELLS_SKIPPED["$cell"]="alpha_fail"
                break
            fi

            out="$branch_results_dir/${ds}-run${run}.json"
            log "[$cell run $run] bench -> $out"
            if ! ( cd "$BENCH_DIR" && go run ./cmd/bench \
                      -mode correctness \
                      -dataset "$BENCH_DIR/datasets/$ds" \
                      -alpha "$ALPHA_GRPC" \
                      -targets "$TARGETS" \
                      -numpaths "$NUMPATHS" \
                      -maxfrontier "$cap" \
                      -concurrency "$CONCURRENCY" \
                      -seed "$SEED" \
                      -timeout "$TIMEOUT" \
                      -out "$out" \
                 ) 2>&1 | tee "$LOG_DIR/bench-$branch-$ds-run${run}.log"
            then
                warn "[$cell run $run] bench invocation failed -- see log"
            fi
            CELLS_RAN["$cell"]=$(( ${CELLS_RAN["$cell"]:-0} + 1 ))
        done

        stop_alpha
    done

    BRANCH_ELAPSED=$(( $(date +%s) - BRANCH_START ))
    log "[$branch] done in ${BRANCH_ELAPSED}s"
done

TOTAL_ELAPSED=$(( $(date +%s) - SCRIPT_START ))

# ============================================================================
# Summary
# ============================================================================
log ""
log "================================================================="
log " sweep complete in ${TOTAL_ELAPSED}s"
log "================================================================="
log "results: $RESULTS_DIR"
log "logs:    $LOG_DIR"
log "master:  $MASTER_LOG"
log ""

log "per-cell status:"
printf '%-14s %-22s %-10s %s\n' "branch" "dataset" "runs" "skip_reason"
printf '%-14s %-22s %-10s %s\n' "------" "-------" "----" "-----------"
for branch in "${BRANCHES[@]}"; do
    for ds in "${DATASETS[@]}"; do
        cell="$branch:$ds"
        ran="${CELLS_RAN[$cell]:-0}"
        skip="${CELLS_SKIPPED[$cell]:-}"
        printf '%-14s %-22s %-10s %s\n' "$branch" "$ds" "$ran/$RUNS_PER_BRANCH" "$skip"
    done
done

log ""
log "per-run JSON summary (passed/failed/p50):"
if command -v jq >/dev/null; then
    printf '%-14s %-22s %-7s %-7s %-7s %-9s\n' "branch" "dataset" "run" "passed" "failed" "p50_ms"
    printf '%-14s %-22s %-7s %-7s %-7s %-9s\n' "------" "-------" "---" "------" "------" "------"
    for branch in "${BRANCHES[@]}"; do
        for ds in "${DATASETS[@]}"; do
            for run in $(seq 1 "$RUNS_PER_BRANCH"); do
                f="$RESULTS_DIR/$branch/${ds}-run${run}.json"
                [[ -f "$f" ]] || continue
                passed=$(jq -r '.passed' "$f" 2>/dev/null || echo "?")
                failed=$(jq -r '.failed' "$f" 2>/dev/null || echo "?")
                p50_ns=$(jq -r '.latency.p50_ns // 0' "$f" 2>/dev/null || echo 0)
                p50_ms=$(awk -v n="$p50_ns" 'BEGIN { printf "%.0f", n/1000000 }')
                printf '%-14s %-22s %-7s %-7s %-7s %-9s\n' "$branch" "$ds" "$run" "$passed" "$failed" "$p50_ms"
            done
        done
    done
fi
