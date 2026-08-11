#!/usr/bin/env bash
# chalkbench-go.sh: run the chalkboard Go benchmarks into one
# benchstat-friendly file.
#
#   scripts/chalkbench-go.sh bench-before.txt     # on chalk/bench (main)
#   scripts/chalkbench-go.sh bench-after.txt      # after the tree swap
#   benchstat bench-before.txt bench-after.txt
#
# The three fixtures are run in SEPARATE processes on purpose: each
# fixture is built lazily and cached for the life of the process, so a
# single combined run leaves ~60MB of large/huge boards resident and the
# GC tax inflates the cheap `default` numbers ~2x. One process per
# fixture keeps each measurement honest.
#
# Knobs: COUNT (default 10), BENCHTIME (default 300ms), STORE_COUNT
# (default 5). The store replay benchmarks always use -benchtime=1x: a
# single M=5000/N=2000 fold already takes >10s.

set -euo pipefail

cd "$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"

OUT="${1:-bench-before.txt}"
COUNT="${COUNT:-10}"
BENCHTIME="${BENCHTIME:-300ms}"
STORE_COUNT="${STORE_COUNT:-5}"

# Benchmarks with no per-fixture sub-benchmark. A two-element -bench
# pattern like '/large' never selects them, so they get their own pass.
SOLO='Lookup|Snapshot_Diff_Small|Render_DefaultTemplates'

: >"$OUT"
echo "# $(date -Is)  $(git rev-parse --short HEAD)  COUNT=$COUNT BENCHTIME=$BENCHTIME" >>"$OUT"

for sel in "$SOLO" /default /large /huge; do
  echo "==> internal/chalkboard $sel" >&2
  go test ./internal/chalkboard -run XXX -bench "$sel" -benchmem \
    -count="$COUNT" -benchtime="$BENCHTIME" >>"$OUT"
done

echo "==> internal/store Chalkboard" >&2
go test ./internal/store -run XXX -bench Chalkboard -benchmem \
  -benchtime=1x -count="$STORE_COUNT" >>"$OUT"

echo "wrote $OUT" >&2
