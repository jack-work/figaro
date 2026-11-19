#!/usr/bin/env bash
set -euo pipefail

sha=${1:?usage: build.sh <sha>}
repo=${FIGARO_REPO:-$HOME/dev/figaro-qua/.bare}
tree=/var/tmp/figaro-approvals-build-$sha
link=$HOME/.local/share/figaro-approvals/figaro

git -C "$repo" fetch -q keel "$sha" 2>/dev/null || git -C "$repo" fetch -q keel
git -C "$repo" cat-file -e "$sha^{commit}"
[ -d "$tree" ] || git -C "$repo" worktree add -q --detach "$tree" "$sha"
[ "$(git -C "$tree" rev-parse HEAD)" = "$(git -C "$repo" rev-parse "$sha^{commit}")" ]

mkdir -p "$(dirname "$link")"
out=$(nix build --no-write-lock-file --print-out-paths --out-link "$link" "$tree#figaro")
"$out/bin/figaro" approvals --help >/dev/null
echo "$out"
