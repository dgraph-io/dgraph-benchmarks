# scripts/lib/alpha.sh -- shared Dgraph Alpha lifecycle + safety helpers.
#
# SOURCE THIS, do not exec. Usage:
#
#   set -euo pipefail
#   BENCH_DIR=...; DGRAPH_REPO=...; ALPHA_DIR=...; ALPHA_DIR_PREFIX_ALLOW=...
#   DATASETS=( kgs datagen-7_5-fb )
#   source "$(dirname "${BASH_SOURCE[0]}")/lib/alpha.sh"
#   trap alpha_cleanup_on_exit EXIT
#
# Required env from the caller (set BEFORE calling any function below):
#   BENCH_DIR                  -- for bulk_p_for() convention path
#   ALPHA_DIR                  -- workspace for alpha (p/t/w/zw + logs)
#   ALPHA_DIR_PREFIX_ALLOW     -- safety whitelist for rm operations
#   ALPHA_HEALTH_URL           -- e.g. http://localhost:8080/health
#   DATASETS                   -- bash array; guard_rm_target uses it to
#                                 protect each dataset's bulk p/ from rm
#
# Optional env (defaults if unset):
#   ALPHA_HEALTH_TIMEOUT_SEC   -- 300 -- wait_alpha timeout
#   ZERO_STATE_URL             -- required for require_zero(no-arg form)
#   BULK_P_<DS_UPPER>          -- override per-dataset bulk p/ path
#                                 (DS uppercased; -/. -> _; e.g. BULK_P_DATAGEN_7_5_FB)
#
# Functions provided:
#   ts log die warn             -- logging
#   size_of <path>              -- human-readable du of one path
#   bulk_p_for <dataset>        -- BULK_P_<DS> env wins else convention path
#   guard_rm_target <path>      -- die if path is unsafe to rm
#   stop_alpha                  -- stop any running alpha (managed + stray)
#   wait_alpha                  -- block until /health healthy, or timeout
#   reset_data <bulk_p>         -- wipe ALPHA_DIR/{p,t,w,zw}, cp bulk_p -> p
#   start_alpha <logfile>       -- launch alpha (background), record pid
#   require_zero [url]          -- die unless Zero is reachable
#   to_seconds <dur>            -- "5m"/"60s"/"90" -> integer seconds
#   preflight_memory            -- abort if RAM already low; warn if no swap
#   start_memlog <logfile>      -- background free/RSS sampler (post-mortem data)
#   stop_memlog                 -- kill the sampler (call from EXIT trap)
#   tail_log <label> <logfile>  -- emit last 50 lines of a log to stderr
#   alpha_cleanup_on_exit       -- EXIT-trap target; stops alpha if we started it
#
# State maintained:
#   RUNNING_ALPHA               -- 0/1 flag, set by start_alpha / stop_alpha
#   MEMLOG_PID                  -- pid of the memory sampler, set by start_memlog
#   /tmp/dgraph-alpha.pid       -- pid file written by start_alpha

ALPHA_HEALTH_TIMEOUT_SEC="${ALPHA_HEALTH_TIMEOUT_SEC:-300}"
RUNNING_ALPHA="${RUNNING_ALPHA:-0}"

ts()   { date +'%Y-%m-%d %H:%M:%S'; }
log()  { echo "[$(ts)] $*"; }
die()  { echo "[$(ts)] ERROR: $*" >&2; exit 1; }
warn() { echo "[$(ts)] WARN: $*" >&2; }

size_of() {
    if [[ -e "$1" ]]; then du -sh "$1" 2>/dev/null | awk '{print $1}'; else echo "(absent)"; fi
}

bulk_p_for() {
    local ds="$1"
    local upper
    upper=$(printf '%s' "$ds" | tr '[:lower:]-.' '[:upper:]__')
    local var="BULK_P_$upper"
    if [[ -n "${!var:-}" ]]; then
        echo "${!var}"
    else
        echo "${BENCH_DIR}/datasets/$ds/dgraph/bulk-out/0/p"
    fi
}

guard_rm_target() {
    local t="$1"
    [[ -n "$t" ]]                              || die "rm target empty"
    [[ "$t" == /* ]]                           || die "rm target not absolute: $t"
    [[ "$t" != "/" ]]                          || die "refusing to rm /"
    [[ "$t" != "$HOME" ]]                      || die "refusing to rm \$HOME ($HOME)"
    [[ -n "${ALPHA_DIR_PREFIX_ALLOW:-}" ]]     || die "ALPHA_DIR_PREFIX_ALLOW unset in caller"
    [[ "$t" == "$ALPHA_DIR_PREFIX_ALLOW"* ]]   || die "rm target not under allowed prefix: $t"
    if declare -p DATASETS >/dev/null 2>&1; then
        for ds in "${DATASETS[@]}"; do
            local bp
            bp=$(bulk_p_for "$ds")
            case "$bp" in
                "$t"|"$t"/*) die "refusing to rm $t -- would clobber BULK_P for $ds ($bp)" ;;
            esac
        done
    fi
}

stop_alpha() {
    if [[ -f /tmp/dgraph-alpha.pid ]]; then
        local pid
        pid=$(cat /tmp/dgraph-alpha.pid)
        if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
            for _ in $(seq 1 30); do
                kill -0 "$pid" 2>/dev/null || { rm -f /tmp/dgraph-alpha.pid; RUNNING_ALPHA=0; return 0; }
                sleep 1
            done
            kill -9 "$pid" 2>/dev/null || true
            sleep 2
        fi
        rm -f /tmp/dgraph-alpha.pid
    fi
    pgrep -af 'dgraph alpha' >/dev/null 2>&1 || { RUNNING_ALPHA=0; return 0; }
    pgrep -af 'dgraph alpha' 2>/dev/null | while IFS= read -r line; do
        local pid cmd
        pid=$(awk '{print $1}' <<< "$line")
        cmd=$(awk '{$1=""; sub(/^ /, ""); print}' <<< "$line")
        if [[ "$cmd" =~ ^(/[^[:space:]]+/)?dgraph[[:space:]]+alpha([[:space:]]|$) ]]; then
            kill "$pid" 2>/dev/null || true
        fi
    done
    sleep 2
    RUNNING_ALPHA=0
}

wait_alpha() {
    [[ -n "${ALPHA_HEALTH_URL:-}" ]] || die "ALPHA_HEALTH_URL unset in caller"
    local deadline=$(( $(date +%s) + ALPHA_HEALTH_TIMEOUT_SEC ))
    while (( $(date +%s) < deadline )); do
        if curl -s -m 3 "$ALPHA_HEALTH_URL" 2>/dev/null | grep -q '"status":"healthy"'; then
            return 0
        fi
        sleep 2
    done
    return 1
}

reset_data() {
    local bulk_p="$1"
    [[ -n "${ALPHA_DIR:-}" ]] || die "ALPHA_DIR unset in caller"
    for d in p t w zw; do
        local target="$ALPHA_DIR/$d"
        guard_rm_target "$target"
        if [[ -e "$target" ]]; then
            log "  rm -rf $target (size $(size_of "$target"))"
            rm -rf "$target"
        fi
    done
    log "  cp -r $bulk_p (size $(size_of "$bulk_p")) -> $ALPHA_DIR/p"
    cp -r "$bulk_p" "$ALPHA_DIR/p"
}

start_alpha() {
    local logfile="$1"
    [[ -n "${ALPHA_DIR:-}" ]] || die "ALPHA_DIR unset in caller"
    ( cd "$ALPHA_DIR"
      # GOMEMLIMIT: Go's soft memory limit. Alpha GCs aggressively before
      # reaching this threshold so RSS stays below the 48G cgroup hard cap.
      # Queries that need more memory slow down and hit the query timeout
      # (counted as timeouts, not wrong answers) rather than alpha ballooning
      # to the cap and being OOM-killed. 40G leaves 8G headroom under MemoryMax.
      GOMEMLIMIT="${GOMEMLIMIT:-40GiB}" nohup dgraph alpha \
          --zero "${ALPHA_ZERO:-localhost:5080}" \
          --limit "query-edge=${ALPHA_QUERY_EDGE_LIMIT:-50000000}" \
          > "$logfile" 2>&1 &
      echo $! > /tmp/dgraph-alpha.pid
    )
    # Make alpha the OOM-killer's first victim. A runaway numpaths>=2 query can
    # balloon memory; without this the kernel may kill the SSH session/sshd
    # instead, which looks like "can't log into the VM". Raising our own
    # process's oom_score_adj is allowed without root. So a runaway kills alpha
    # (recoverable, logged as a query error) and leaves sshd alone.
    local apid
    apid=$(cat /tmp/dgraph-alpha.pid 2>/dev/null || true)
    if [[ -n "$apid" ]]; then
        echo "${ALPHA_OOM_SCORE_ADJ:-900}" > "/proc/$apid/oom_score_adj" 2>/dev/null \
            || warn "could not set oom_score_adj for alpha pid $apid (continuing)"
    fi
    RUNNING_ALPHA=1
}

require_zero() {
    local url="${1:-${ZERO_STATE_URL:-}}"
    [[ -n "$url" ]] || die "require_zero: no URL passed and ZERO_STATE_URL unset"
    curl -s -m 5 "$url" >/dev/null 2>&1 \
        || die "zero is not reachable at $url -- start it before running"
}

to_seconds() {
    local v="$1"
    case "$v" in
        *m) echo $(( ${v%m} * 60 )) ;;
        *s) echo "${v%s}" ;;
        *)  echo "$v" ;;
    esac
}

# Pre-flight memory check (RCA 5.4). The VM-freeze incident started from runs
# launched while a previous run's alpha was still resident: abort early instead
# of starting a sweep that will hit the memory ceiling an hour in. Also verify
# swap survived the last reboot -- no swap means RAM exhaustion freezes the VM
# instead of OOM-killing alpha (RCA-1).
preflight_memory() {
    [[ -r /proc/meminfo ]] || { warn "preflight_memory: /proc/meminfo unreadable -- skipping check"; return 0; }
    local min_mb="${MIN_AVAIL_MB:-8192}"
    local avail_mb swap_total_mb swap_free_mb
    avail_mb=$(awk '/^MemAvailable/ {print int($2/1024)}' /proc/meminfo)
    swap_total_mb=$(awk '/^SwapTotal/ {print int($2/1024)}' /proc/meminfo)
    swap_free_mb=$(awk '/^SwapFree/ {print int($2/1024)}' /proc/meminfo)
    log "preflight: available RAM ${avail_mb} MB | swap free ${swap_free_mb}/${swap_total_mb} MB"
    if (( swap_total_mb == 0 )); then
        warn "NO SWAP configured -- RAM exhaustion will freeze the whole VM, not just alpha (see dgraph-bench-rca)"
    fi
    if (( avail_mb < min_mb )); then
        die "only ${avail_mb} MB RAM available (< ${min_mb} MB) -- a previous run is likely still resident; refusing to start"
    fi
}

# Background memory sampler (RCA 5.3): free + dgraph RSS every
# MEMLOG_INTERVAL_SEC (default 10s). Gives post-mortem data even if the run
# dies -- the last lines show whether we were climbing into the ceiling.
start_memlog() {
    local memlog="$1"
    (
      while true; do
        {
          echo "=== $(date -u +'%Y-%m-%d %H:%M:%S') ==="
          free -m 2>/dev/null | grep -E 'Mem|Swap'
          # shellcheck disable=SC2009  # pgrep can't give RSS+CPU in one shot
          ps aux --sort=-%mem 2>/dev/null | grep '[d]graph' | head -3 \
              | awk '{printf "%s pid=%s cpu=%s%% rss=%.0fMB\n", $11" "$12, $2, $3, $6/1024}'
        } >> "$memlog"
        sleep "${MEMLOG_INTERVAL_SEC:-10}"
      done
    ) &
    MEMLOG_PID=$!
    log "memlog: sampling every ${MEMLOG_INTERVAL_SEC:-10}s -> $memlog (pid $MEMLOG_PID)"
}

stop_memlog() {
    if [[ -n "${MEMLOG_PID:-}" ]] && kill -0 "$MEMLOG_PID" 2>/dev/null; then
        kill "$MEMLOG_PID" 2>/dev/null || true
    fi
    MEMLOG_PID=""
}

# Last-50-lines dump for failure paths (RCA 5.6): when alpha goes unhealthy or
# a bench invocation fails, the cause is usually in the alpha log -- surface it
# in the master log instead of making the operator go hunting.
tail_log() {
    local label="$1" logfile="$2"
    [[ -f "$logfile" ]] || { warn "$label: no log at $logfile"; return 0; }
    warn "$label: last 50 lines of $logfile:"
    tail -50 "$logfile" >&2 || true
}

alpha_cleanup_on_exit() {
    if [[ ${RUNNING_ALPHA:-0} -eq 1 ]]; then
        log "cleanup_on_exit: stopping alpha we started"
        stop_alpha 2>/dev/null || true
    fi
}
