#!/usr/bin/env bash
# prefixcorpus.sh: build the fixture a hop measurement needs -- one parent with
# a long history, one branch cut out of its middle, and a SECOND branch cut at
# the same turn (the cousin case). No credentials, no tokens: the replies come
# from scripts/fake-gateway-bulk.py.
#
#   scripts/prefixcorpus.sh [turns] [forkturn] [reply_bytes]
#
# Writes /tmp/figaro-prefix/ids.env: BIN, BOX, PARENT, BRANCH, COUSIN and the
# environment a later run must export to see the same store.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BOX=${BOX:-/tmp/figaro-prefix}
BIN=$BOX/figaro
PORT=${PORT:-8931}
TURNS=${1:-40}
FORK=${2:-21}
SIZE=${3:-3000}

rm -rf "$BOX"; mkdir -p "$BOX"/{cfg/outfits,cfg/providers,state,rt,cache}
cleanup() { [ -z "${GW_PID:-}" ] || kill "$GW_PID" 2>/dev/null || true; }
trap cleanup EXIT

echo "== building from $(git -C "$ROOT" rev-parse --short HEAD)"
( cd "$ROOT" && go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" -o "$BIN" ./cmd/figaro ) || exit 1

python3 "$ROOT/scripts/fake-gateway-bulk.py" "$PORT" "$BOX/requests.jsonl" "$SIZE" & GW_PID=$!
sleep 1

cat > "$BOX/cfg/providers/gateway.toml" <<EOF
base_url = "http://127.0.0.1:$PORT/v1"
EOF
printf 'You are a test agent.\n' > "$BOX/cfg/credo.md"
cat > "$BOX/cfg/outfits/bulk.toml" <<'EOF'
duke-title = "measurer"

[system]
provider   = "gateway"
model      = "auto"
max_tokens = 4096
credo      = { fileName = "credo.md" }
EOF
cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "bulk"
interactive = false
EOF

export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state \
       FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1

PARENT=$("$BIN" new -j | python3 -c 'import json,sys; print(json.load(sys.stdin)["aria_id"])') || exit 1
echo "== parent $PARENT: $TURNS turns of ~$SIZE bytes"
for i in $(seq 1 "$TURNS"); do
  "$BIN" send -r --id "$PARENT" -- "turn $i: say your piece" >/dev/null || exit 1
done

echo "== branch at :$FORK"
BRANCH=$("$BIN" fork "$PARENT:$FORK" --stay -j | python3 -c 'import json,sys; print(json.load(sys.stdin)["alternative"])') || exit 1
"$BIN" send -r --id "$BRANCH" -- "branch: say your piece" >/dev/null || exit 1
for i in 1 2; do
  "$BIN" send -r --id "$BRANCH" -- "branch turn $i" >/dev/null || exit 1
done

echo "== cousin at :$FORK"
COUSIN=$("$BIN" fork "$PARENT:$FORK" --stay -j | python3 -c 'import json,sys; print(json.load(sys.stdin)["alternative"])') || exit 1
"$BIN" send -r --id "$COUSIN" -- "cousin: say your piece" >/dev/null || exit 1
for i in 1 2; do
  "$BIN" send -r --id "$COUSIN" -- "cousin turn $i" >/dev/null || exit 1
done

# Count committed turns, not a transient idle status sampled after acceptance.
for pair in "$PARENT:$TURNS" "$BRANCH:$((FORK + 2))" "$COUSIN:$((FORK + 2))"; do
  id=${pair%:*}; want=${pair##*:}
  "$BIN" show "$id" -a -j | python3 -c 'import json,sys
want=int(sys.argv[1]); parts=json.load(sys.stdin)["parts"]
ids={p["turn"] for p in parts}
if any(not p.get("nodes") for p in parts):
    sys.exit("fixture: a turn has no reply")
if len(ids) != want or max(ids, default=0) != want:
    sys.exit("fixture: expected %d turns, got %d (last %d)" % (want, len(ids), max(ids, default=0)))' "$want" || exit 1
done

cat > "$BOX/ids.env" <<EOF
BOX=$BOX
BIN=$BIN
PARENT=$PARENT
BRANCH=$BRANCH
COUSIN=$COUSIN
FORKTURN=$FORK
TURNS=$TURNS
export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state
export FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1
EOF
echo "== ids in $BOX/ids.env"
cat "$BOX/ids.env"
"$BIN" ls -g -a
du -sh "$BOX/state"
