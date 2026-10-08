#!/usr/bin/env bash
# scratch-gc.sh: prune the scratch refs a worked branch leaves behind.
#
# See skills/figaro/contributing/maintaining.md, "Commit constantly, present
# deliberately". A scratch ref holds the WIP history a presented commit was
# rebuilt from: it is a safety net, it is never merged, and it is garbage once
# the work it protected has landed.
#
#   scripts/scratch-gc.sh                 # report what is prunable, delete nothing
#   scripts/scratch-gc.sh --days 14       # a different horizon
#   scripts/scratch-gc.sh --prune         # actually delete
#   scripts/scratch-gc.sh --prune --merged main
#
# DRY BY DEFAULT. A sweeper that deletes on its first run teaches you to not
# run it.
set -uo pipefail

DAYS=7
PRUNE=0
MERGED=""
PREFIX="refs/heads/scratch/"

usage() {
  sed -n '2,13p' "$0" | sed 's/^# \?//'
  exit "${1:-0}"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --days) DAYS="${2:?--days <n>}"; shift 2 ;;
    --prune) PRUNE=1; shift ;;
    --dry-run) PRUNE=0; shift ;;
    --merged) MERGED="${2:?--merged <branch>}"; shift 2 ;;
    --prefix) PREFIX="refs/heads/${2:?--prefix <dir>}/"; shift 2 ;;
    -h|--help) usage 0 ;;
    *) echo "scratch-gc: unknown flag $1" >&2; usage 1 ;;
  esac
done

git rev-parse --git-dir >/dev/null 2>&1 || {
  echo "scratch-gc: not a git repository" >&2; exit 1
}

cutoff=$(( $(date +%s) - DAYS * 86400 ))
kept=0 doomed=0

# A ref still checked out by a worktree is live work, whatever its age: the
# sweeper must never delete the branch someone is standing on.
checked_out="$(git worktree list --porcelain | sed -n 's/^branch //p')"

while IFS=$'\t' read -r ref when subject; do
  [ -n "$ref" ] || continue
  name="${ref#refs/heads/}"
  age_days=$(( ( $(date +%s) - when ) / 86400 ))
  if printf '%s\n' "$checked_out" | grep -qxF "$ref"; then
    printf 'keep   %-44s %3dd  checked out\n' "$name" "$age_days"
    kept=$((kept + 1))
    continue
  fi
  if [ -n "$MERGED" ] && ! git merge-base --is-ancestor "$ref" "$MERGED" 2>/dev/null; then
    printf 'keep   %-44s %3dd  not in %s\n' "$name" "$age_days" "$MERGED"
    kept=$((kept + 1))
    continue
  fi
  if [ "$when" -gt "$cutoff" ]; then
    printf 'keep   %-44s %3dd  younger than %dd\n' "$name" "$age_days" "$DAYS"
    kept=$((kept + 1))
    continue
  fi
  doomed=$((doomed + 1))
  if [ "$PRUNE" = 1 ]; then
    # -D, not -d: a scratch ref is deliberately never merged, so -d refuses
    # every one of them. The reflog keeps the sha reachable for 90 days.
    sha="$(git rev-parse --short "$ref")"
    git branch -D "$name" >/dev/null && printf 'prune  %-44s %3dd  was %s\n' "$name" "$age_days" "$sha"
  else
    printf 'PRUNE  %-44s %3dd  %s\n' "$name" "$age_days" "${subject:0:40}"
  fi
done < <(git for-each-ref --sort=committerdate \
  --format='%(refname)%09%(committerdate:unix)%09%(contents:subject)' "$PREFIX")

if [ "$doomed" = 0 ] && [ "$kept" = 0 ]; then
  echo "scratch-gc: no refs under ${PREFIX#refs/heads/}"
  exit 0
fi
if [ "$PRUNE" = 1 ]; then
  echo "scratch-gc: pruned $doomed, kept $kept (sha stays in the reflog ~90 days)"
else
  echo "scratch-gc: $doomed prunable, $kept kept. --prune to delete."
fi
