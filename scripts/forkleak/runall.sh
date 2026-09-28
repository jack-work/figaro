#!/usr/bin/env bash
# fan the three shapes out over seeds
set -u
tell() { printf '%s\n' "$*" >> /var/tmp/forkleak/verdicts.txt; }
run() { # run <script> <seed>
  local s=$1 n=$2 out code
  out=$(timeout 480 bash /var/tmp/forkleak/$s.sh $n 2>&1); code=$?
  printf '%s\n' "$out" > /var/tmp/forkleak/log-$s-$n.txt
  case $code in
    0) v=clean;; 1) v=LEAK;; 2) v=STORE-LEAK;; 3) v=setup;; 4) v=hop;; 124) v=timeout;; *) v=exit$code;;
  esac
  tell "$(printf '%-10s %-5s %-10s %s' "$s" "$n" "$v" "$(printf '%s\n' "$out" | head -1)")"
}
export -f run tell
for s in fuzz1 fuzz2 fuzz3; do for n in $(seq "$1" "$2"); do echo "$s $n"; done; done |
  xargs -P "${3:-5}" -n 2 bash -c 'run "$0" "$1"'
