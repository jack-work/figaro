#!/usr/bin/env bash
# Run a batch of fuzz trials in parallel. Usage: batch.sh <first-seed> <count> [par]
set -uo pipefail
first=${1:-1} count=${2:-8} par=${3:-4}
seq "$first" $((first + count - 1)) | xargs -P "$par" -I{} bash -c '
  out=$(timeout 420 bash /var/tmp/forkleak/fuzz1.sh {} 2>&1)
  code=$?
  printf "%s\n" "$out" | tee /var/tmp/forkleak/log-{}.txt >/dev/null
  case $code in
    0) verdict=clean ;;
    1) verdict=LEAK ;;
    3) verdict=setup-fail ;;
    4) verdict=hop-fail ;;
    124) verdict=timeout ;;
    *) verdict="exit$code" ;;
  esac
  printf "%-12s %s\n" "$verdict" "$(printf "%s\n" "$out" | head -1)"
'
