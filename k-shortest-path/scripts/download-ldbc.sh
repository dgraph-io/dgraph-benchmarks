#!/usr/bin/env bash
# Downloads and extracts an LDBC Graphalytics dataset (data archive + optional
# validation archive) into ./datasets/<name>/.
#
# Usage:
#   ./scripts/download-ldbc.sh <data-url> [validation-url]
#
# Examples:
#   # Just the data archive (use this if validation is bundled inside, like the
#   # test-sssp-* graphs):
#   ./scripts/download-ldbc.sh \
#       https://example.org/path/to/kgs.tar.zst
#
#   # Data + separate validation archive:
#   ./scripts/download-ldbc.sh \
#       https://example.org/path/to/kgs.tar.zst \
#       https://example.org/path/to/kgs-validation.tar.zst
#
# Dataset names are derived from the data URL's basename. Whichever URL you
# point at is whichever URL you get — there is no hard-coded mirror.
#
# Browse the LDBC dataset catalog at:
#   https://ldbcouncil.org/benchmarks/graphalytics/

set -euo pipefail

if [[ $# -lt 1 ]]; then
    cat >&2 <<EOF
usage: $0 <data-url> [validation-url]

  data-url        URL of the dataset tar.zst (required)
  validation-url  URL of the validation tar.zst (optional — many datasets
                  bundle SSSP/BFS/etc. reference outputs inside the data
                  archive, in which case omit this argument)

Examples:
  $0 https://example.org/path/to/kgs.tar.zst
  $0 https://example.org/path/to/kgs.tar.zst https://example.org/path/to/kgs-validation.tar.zst
EOF
    exit 2
fi

DATA_URL="$1"
VAL_URL="${2:-}"

# Derive dataset name from data URL basename (strip .tar.zst suffix).
DATA_BASENAME="$(basename "$DATA_URL")"
DATASET="${DATA_BASENAME%.tar.zst}"
DATASET="${DATASET%.tar.gz}"

OUT_DIR="$(cd "$(dirname "$0")/.." && pwd)/datasets/$DATASET"
mkdir -p "$OUT_DIR"
cd "$OUT_DIR"

download_one() {
    local url="$1"
    local fname
    fname="$(basename "$url")"
    if [[ -f "$fname" ]]; then
        echo "[skip] $fname already present"
        return 0
    fi
    echo "[download] $url"
    if ! curl -L --fail --connect-timeout 30 -o "$fname" "$url"; then
        echo "[error] failed to download $url" >&2
        rm -f "$fname"
        return 1
    fi
}

extract_one() {
    local fname="$1"
    if [[ ! -f "$fname" ]]; then
        return 0
    fi
    echo "[extract] $fname"
    tar --zstd -xf "$fname"
}

# Download both archives (validation is optional).
download_one "$DATA_URL"
if [[ -n "$VAL_URL" ]]; then
    download_one "$VAL_URL"
fi

# Extract whatever landed on disk.
extract_one "$DATA_BASENAME"
if [[ -n "$VAL_URL" ]]; then
    extract_one "$(basename "$VAL_URL")"
fi

# Normalize: bench expects validation/<name>-SSSP. If the SSSP reference was
# bundled at the top level of the data archive, move it into validation/.
SSSP_TOP="$DATASET-SSSP"
if [[ -f "$SSSP_TOP" ]]; then
    mkdir -p validation
    mv "$SSSP_TOP" validation/
    echo "[normalize] moved $SSSP_TOP into validation/"
fi

echo "[done] dataset ready at $OUT_DIR"
ls -lh "$OUT_DIR"
echo
echo "Contents under validation/ (if any):"
ls -lh "$OUT_DIR/validation" 2>/dev/null || echo "  (no validation/ subdirectory — SSSP reference may be elsewhere or absent)"
