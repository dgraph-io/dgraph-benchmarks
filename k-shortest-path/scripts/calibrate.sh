#!/usr/bin/env bash
# calibrate.sh -- empirical maxfrontiersize calibration, per dataset.
#
# WHY: a `maxfrontiersize` value that's too high never triggers eviction
# (bug-fix code path never exercised) and a value that's too low forces
# even a correct k-shortest implementation to fail (the cap is so tight
# the optimum can't be kept alive). The right value sits between the two,
# and it differs per dataset because frontier sizes scale with graph
# degree and diameter. KGS and datagen-7_5-fb cannot share a cap.
#
# WHAT: against a single known-correct branch (default pr-9678 -- the
# Family A PR with the most tests per README), this script sweeps cap
# values per dataset and reports the smallest cap where correctness
# still holds on numpaths=2. That's the recommended MAXFRONTIER_<DS>:
# tight enough to trigger eviction on as many queries as possible,
# loose enough that a real fix can still produce the right answer.
#
# USAGE: run AFTER probe.sh has identified at least one PR that passes
# numpaths=2. Set CALIBRATION_BRANCH to that PR. Re-run after any fresh
# bulk-load.
#
# OUTPUT: prints a per-dataset table and a copy-pasteable
# .env-style block of MAXFRONTIER_<DS>=<value> recommendations.
#
# Override anything via env:
#   BENCH_DIR DGRAPH_REPO ALPHA_DIR ALPHA_DIR_PREFIX_ALLOW
#   CALIBRATION_BRANCH    (default pr-9678)
#   DATASETS_OVERRIDE     (default "kgs datagen-7_5-fb")
#   CAPS_OVERRIDE         (space-separated; 0 means "unset/no cap")
#                         default "50 100 200 500 1000 2000 5000 10000 20000 50000 100000 0"
#   TARGETS               (default 20 -- per-cap sample size)
#   NUMPATHS              (default 2 -- bug-triggering value)
#   SEED                  (default 1)
#   TIMEOUT               (default 5m per query)

set -euo pipefail

# ============================================================================
# CONFIG
# ============================================================================
BENCH_DIR="${BENCH_DIR:-/Users/shiva/workspace/shortest-path-bench}"
DGRAPH_REPO="${DGRAPH_REPO:-/Users/shiva/workspace/dgraph-scratch/dgraph}"
ALPHA_DIR="${ALPHA_DIR:-/Users/shiva/workspace/db}"
ALPHA_DIR_PREFIX_ALLOW="${ALPHA_DIR_PREFIX_ALLOW:-/Users/shiva/workspace/}"

CALIBRATION_BRANCH="${CALIBRATION_BRANCH:-pr-9678}"

DATASETS_STR="${DATASETS_OVERRIDE:-kgs datagen-7_5-fb}"
read -ra DATASETS <<< "$DATASETS_STR"

# 0 sentinel = "unset" (no cap). Sweep is geometric from low to high.
CAPS_STR="${CAPS_OVERRIDE:-50 100 200 500 1000 2000 5000 10000 20000 50000 100000 0}"
read -ra CAPS <<< "$CAPS_STR"

TARGETS="${TARGETS:-20}"
NUMPATHS="${NUMPATHS:-2}"
SEED="${SEED:-1}"
TIMEOUT="${TIMEOUT:-5m}"

ALPHA_HTTP_URL="${ALPHA_HTTP_URL:-http://localhost:8080}"
ALPHA_HEALTH_URL="$ALPHA_HTTP_URL/health"
ALPHA_GRPC="${ALPHA_GRPC:-localhost:9080}"
ZERO_STATE_URL="${ZERO_STATE_URL:-http://localhost:6080/state}"
ALPHA_HEALTH_TIMEOUT_SEC="${ALPHA_HEALTH_TIMEOUT_SEC:-300}"

RESULTS_DIR="${RESULTS_DIR:-$BENCH_DIR/results/calibrate}"
LOG_DIR="$RESULTS_DIR/logs"
mkdir -p "$RESULTS_DIR" "$LOG_DIR"

# Shared Alpha lifecycle + safety helpers (stop_alpha, wait_alpha, reset_data,
# start_alpha, guard_rm_target, bulk_p_for, log/die/warn, etc.)
source "$(dirname "${BASH_SOURCE[0]}")/lib/alpha.sh"
trap alpha_cleanup_on_exit EXIT

# ============================================================================
# calibrate-specific helpers
# ============================================================================
env_name_for() {
    local ds="$1"
    printf 'MAXFRONTIER_%s' "$(printf '%s' "$ds" | tr '[:lower:]-.' '[:upper:]__')"
}

# ============================================================================
# Pre-flight
# ============================================================================
log "================================================================="
log " calibrate.sh -- pick MAXFRONTIER per dataset"
log "================================================================="
log "config:"
log "  CALIBRATION_BRANCH = $CALIBRATION_BRANCH"
log "  DATASETS           = ${DATASETS[*]}"
log "  CAPS sweep         = ${CAPS[*]}"
log "  TARGETS / cell     = $TARGETS"
log "  NUMPATHS           = $NUMPATHS"
log "  SEED               = $SEED"
log "  per-query TIMEOUT  = $TIMEOUT"

for bin in dgraph go git make curl jq awk; do
    command -v "$bin" >/dev/null || die "$bin not on PATH"
done

[[ -d "$BENCH_DIR" ]]   || die "BENCH_DIR not found: $BENCH_DIR"
[[ -d "$DGRAPH_REPO" ]] || die "DGRAPH_REPO not found: $DGRAPH_REPO"
[[ -d "$ALPHA_DIR" ]]   || die "ALPHA_DIR not found: $ALPHA_DIR"
[[ "$ALPHA_DIR" == "$ALPHA_DIR_PREFIX_ALLOW"* ]] \
    || die "ALPHA_DIR ($ALPHA_DIR) not under allowed prefix $ALPHA_DIR_PREFIX_ALLOW"
[[ "$ALPHA_DIR" != "/" ]]    || die "ALPHA_DIR is /"
[[ "$ALPHA_DIR" != "$HOME" ]] || die "ALPHA_DIR equals HOME"

curl -s -m 5 "$ZERO_STATE_URL" >/dev/null 2>&1 \
    || die "zero not reachable at $ZERO_STATE_URL -- start it before calibrating"

( cd "$DGRAPH_REPO"
  if ! git diff --quiet || ! git diff --cached --quiet; then
      die "dgraph working tree dirty -- commit/stash before calibrating"
  fi
  git rev-parse --verify --quiet "$CALIBRATION_BRANCH" >/dev/null \
      || die "calibration branch '$CALIBRATION_BRANCH' not found in $DGRAPH_REPO"
)

for ds in "${DATASETS[@]}"; do
    ds_dir="$BENCH_DIR/datasets/$ds"
    [[ -f "$ds_dir/$ds.properties" ]] || die "$ds_dir/$ds.properties missing"
    bp=$(bulk_p_for "$ds")
    [[ -d "$bp" ]] || die "bulk-loaded p/ missing for $ds at $bp"
done

( cd "$BENCH_DIR" && go build ./... ) || die "bench failed to compile"

# ============================================================================
# Build calibration branch
# ============================================================================
log ""
log "[build] checkout + make install for $CALIBRATION_BRANCH"
( cd "$DGRAPH_REPO"
  git checkout "$CALIBRATION_BRANCH" >/dev/null 2>&1
  make install ) > "$LOG_DIR/build.log" 2>&1 \
    || die "build failed -- see $LOG_DIR/build.log"

bin_branch=$(dgraph version 2>/dev/null | awk '/^Branch/ {print $3; exit}' || true)
if [[ -n "$bin_branch" && "$bin_branch" != "$CALIBRATION_BRANCH" ]]; then
    log "[build] WARN: dgraph binary reports Branch='$bin_branch' (expected '$CALIBRATION_BRANCH')"
fi
log "[build] done"

stop_alpha

# ============================================================================
# Calibration loop
# ============================================================================
# RESULT[ds:cap] -> "passed failed errors p50_ms wall_s"
declare -A RESULT

t0=$(date +%s)

for ds in "${DATASETS[@]}"; do
    bp=$(bulk_p_for "$ds")
    log ""
    log "================================================================="
    log " dataset: $ds  (bulk p/ at $bp)"
    log "================================================================="

    # Bring Alpha up ONCE per dataset (data doesn't change as cap varies).
    stop_alpha
    log "[$ds] reset_data + start alpha"
    reset_data "$bp"
    start_alpha "$LOG_DIR/alpha-$ds.log"
    if ! wait_alpha; then
        log "[$ds] alpha did not come up healthy; skipping dataset"
        for cap in "${CAPS[@]}"; do RESULT["$ds:$cap"]="ALPHA_FAIL"; done
        continue
    fi
    log "[$ds] alpha healthy"

    for cap in "${CAPS[@]}"; do
        out="$RESULTS_DIR/cal-$ds-cap${cap}.json"
        bench_log="$LOG_DIR/bench-$ds-cap${cap}.log"
        log "[$ds cap=$cap] running bench (targets=$TARGETS numpaths=$NUMPATHS)"

        if ! ( cd "$BENCH_DIR" && go run ./cmd/bench \
              -mode correctness \
              -dataset "$BENCH_DIR/datasets/$ds" \
              -alpha "$ALPHA_GRPC" \
              -targets "$TARGETS" \
              -numpaths "$NUMPATHS" \
              -maxfrontier "$cap" \
              -seed "$SEED" \
              -timeout "$TIMEOUT" \
              -out "$out" ) > "$bench_log" 2>&1
        then
            log "[$ds cap=$cap] bench invocation failed -- see $bench_log"
            RESULT["$ds:$cap"]="BENCH_FAIL"
            continue
        fi

        passed=$(jq -r '.passed'                "$out" 2>/dev/null || echo 0)
        failed=$(jq -r '.failed'                "$out" 2>/dev/null || echo 0)
        errors=$(jq -r '.query_errors'          "$out" 2>/dev/null || echo 0)
        p50_ns=$(jq -r '.latency.p50_ns // 0'   "$out" 2>/dev/null || echo 0)
        wall_ns=$(jq -r '.latency.wall_ns // 0' "$out" 2>/dev/null || echo 0)
        p50_ms=$(( p50_ns / 1000000 ))
        wall_s=$(awk -v n="$wall_ns" 'BEGIN { printf "%.1f", n/1e9 }')
        RESULT["$ds:$cap"]="$passed $failed $errors $p50_ms $wall_s"
        log "[$ds cap=$cap] passed=$passed/$TARGETS failed=$failed errors=$errors p50=${p50_ms}ms wall=${wall_s}s"
    done

    stop_alpha
done

t1=$(date +%s)
elapsed=$(( t1 - t0 ))

# ============================================================================
# Analysis: per dataset, smallest cap where passed=TARGETS, failed=0, errors=0
# ============================================================================
log ""
log "================================================================="
log " calibration analysis (elapsed: ${elapsed}s)"
log "================================================================="
log " sweep used branch '$CALIBRATION_BRANCH'; TARGETS=$TARGETS NUMPATHS=$NUMPATHS"

declare -A RECOMMENDATION

for ds in "${DATASETS[@]}"; do
    echo ""
    printf "Dataset: %s\n" "$ds"
    printf "  %-8s %-13s %-7s %-7s %-9s %-9s %s\n" \
        "cap" "passed/total" "failed" "errors" "p50_ms" "wall_s" "verdict"
    printf "  %-8s %-13s %-7s %-7s %-9s %-9s %s\n" \
        "--------" "-------------" "------" "------" "------" "------" "-------"

    # Sort caps numerically ascending. Treat "0" (unset) as largest sentinel.
    sorted_caps=$(printf '%s\n' "${CAPS[@]}" | awk '{ if ($1==0) print 999999999, "0"; else print $1, $1 }' \
        | sort -n | awk '{print $2}')

    first_pass=""
    while IFS= read -r cap; do
        v="${RESULT[$ds:$cap]:-MISSING}"
        case "$v" in
            ALPHA_FAIL|BENCH_FAIL|MISSING)
                cap_disp="$cap"; [[ "$cap" == "0" ]] && cap_disp="(none)"
                printf "  %-8s %-13s %-7s %-7s %-9s %-9s %s\n" \
                    "$cap_disp" "-" "-" "-" "-" "-" "$v"
                continue
                ;;
        esac
        read -r p f e p50 wall <<< "$v"
        cap_disp="$cap"; [[ "$cap" == "0" ]] && cap_disp="(none)"
        verdict="✗ fails"
        if (( p == TARGETS )) && (( f == 0 )) && (( e == 0 )); then
            if [[ -z "$first_pass" ]]; then
                verdict="✓ PASSING (smallest)"
                first_pass="$cap"
            else
                verdict="✓ passing"
            fi
        fi
        printf "  %-8s %-13s %-7s %-7s %-9s %-9s %s\n" \
            "$cap_disp" "$p/$TARGETS" "$f" "$e" "$p50" "$wall" "$verdict"
    done <<< "$sorted_caps"

    if [[ -z "$first_pass" ]]; then
        echo "  -> no cap passed correctness on this dataset"
        echo "     (calibration branch '$CALIBRATION_BRANCH' may not actually fix the bug for $ds,"
        echo "      OR every cap in the sweep is too tight for this dataset's frontier scale)"
        RECOMMENDATION["$ds"]=""
    elif [[ "$first_pass" == "0" ]]; then
        echo "  -> recommended: leave $(env_name_for "$ds") UNSET (no cap)"
        echo "     (even the smallest cap tested broke correctness; the cap cannot be exercised"
        echo "      on this dataset without losing the optimum)"
        RECOMMENDATION["$ds"]="UNSET"
    else
        echo "  -> recommended: $(env_name_for "$ds")=$first_pass"
        RECOMMENDATION["$ds"]="$first_pass"
    fi
done

# ============================================================================
# Final copy-pasteable .env block
# ============================================================================
echo ""
log "================================================================="
log " copy-paste into your .env (or export before run-pr-comparison.sh)"
log "================================================================="
echo ""
for ds in "${DATASETS[@]}"; do
    val="${RECOMMENDATION[$ds]:-}"
    name=$(env_name_for "$ds")
    if [[ -z "$val" ]]; then
        echo "# $name  -- no recommendation (calibration inconclusive for $ds)"
    elif [[ "$val" == "UNSET" ]]; then
        echo "# $name  -- intentionally unset (cap cannot be safely exercised on $ds)"
    else
        echo "export $name=$val"
    fi
done
echo ""
log "raw json per (dataset, cap) in $RESULTS_DIR/"
log "alpha logs in $LOG_DIR/"
