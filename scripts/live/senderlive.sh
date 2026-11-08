#!/usr/bin/env bash
# Run on every change to what figaro sends a provider (encoders, reminders,
# sender rendering, system prompt assembly). A unit test proves the bytes an
# encoder writes; only a real model proves it reads them. Each provider:model
# gets three turns on a fresh aria, wire dumped:
#
#   1. from the owner:          no sender reminder on the wire; the model says USER
#   2. from another figaro:     exactly one sender reminder, ahead of the text;
#                               the model names that figaro's handle
#   3. from the owner again:    turn 2's reminder and text are resent byte for
#                               byte, so the cached prefix holds
#
# Usage: scripts/live/senderlive.sh [provider:model ...]
# With no arguments it runs the matrix below. Costs a few cents per model.
# Builds this tree, runs it on an isolated store under /var/tmp, never touches
# your daemon. The store is kept on failure; KEEP=1 keeps it on success too.
set -uo pipefail
cd "$(dirname "$0")/../.."

MATRIX=(
  anthropic:claude-haiku-4-5-20251001
  copilot:claude-haiku-4.5
  copilot:gpt-6.1-sol
  copilot:grok-4.7
  meta:muse-spark-1.3
  rou2:explabs/glm-5
  rou2:explabs/gemini-3.5-flash
)
[ $# -gt 0 ] && MATRIX=("$@")
TURN_TIMEOUT=${TURN_TIMEOUT:-240}

ROOT=$(mktemp -d /var/tmp/figsender.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt" "$ROOT/wire"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
rm -rf "$ROOT/config/troupes" "$ROOT/config/outfits" "$ROOT/config/loadouts"
mkdir -p "$ROOT/config/outfits"
{
  echo 'default_outfit = "m1"'
  awk '/^\[/{skip=($0 ~ /^\[(door|troupe)/)} !skip' "${HOME}/.config/figaro/config.toml" | grep -v '^default_outfit'
} >"$ROOT/config/config.toml"
printf 'You are a terse test agent. Never call a tool. Answer in one line.\n' >"$ROOT/config/credo.md"
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT FIGARO_CALLER
trap '"$BIN" stop >/dev/null 2>&1' EXIT

outfit() {
  cat >"$ROOT/config/outfits/m$1.toml" <<EOF
[system]
provider = "$2"
model = "$3"
max_tokens = 4096
credo = { fileName = "credo.md" }
EOF
}
first=${MATRIX[0]}
outfit 1 "${first%%:*}" "${first#*:}"

ASK='Who sent you this latest message? If a sender is named for it, reply with that sender'"'"'s name exactly as given. If none is named, reply with exactly USER. Nothing else.'
CLOSE='Reply with exactly OK.'

turns() { "$BIN" status --id "$1" -j 2>/dev/null | jq -r '.message_count // 0'; }

wait_turn() {
  local id=$1 before=$2 deadline=$((SECONDS + TURN_TIMEOUT)) st n
  while [ $SECONDS -lt $deadline ]; do
    st=$("$BIN" status --id "$id" -j 2>/dev/null)
    n=$(jq -r '.message_count // 0' <<<"$st")
    [ "$n" -gt "$before" ] && [ "$(jq -r .state <<<"$st")" = idle ] && return 0
    sleep 2
  done
  return 1
}

reply() {
  "$BIN" show --id "$1" -n 1 -j 2>/dev/null |
    jq -r '[.parts[-1].nodes[] | select(.type=="prose" and .role=="output") | .markdown] | join(" ")'
}

newest_req() { ls "$ROOT/wire/$1"/*.req.http 2>/dev/null | sort -V | tail -1; }

S=$("$BIN" new -j 2>/dev/null | jq -r .aria_id)
[ -n "$S" ] && [ "$S" != null ] || { echo "FAIL: could not mint the sending aria"; exit 1; }
SH=$("$BIN" status --id "$S" -j | jq -r .handle)
SS=$("$BIN" status --id "$S" -j | jq -r .short)
echo "sender: $SH ($SS)"

fails=0
printf '\n%-36s %-10s %-6s %-6s %-6s %-6s %s\n' MODEL ROUTE WIRE OWNER NAMED CACHE NOTE
i=0
for pm in "${MATRIX[@]}"; do
  i=$((i + 1))
  prov=${pm%%:*} model=${pm#*:}
  outfit "$i" "$prov" "$model"
  T=$("$BIN" new -j -O "m$i" 2>/dev/null | jq -r .aria_id)
  "$BIN" set --id "$T" system.environment.figaro_wire_dir "$ROOT/wire" >/dev/null 2>&1 || { echo "FAIL $pm: set wire dir"; fails=$((fails + 1)); continue; }

  note=""
  run() {
    local who=$1 text=$2 before
    before=$(turns "$T")
    if [ -n "$who" ]; then
      FIGARO_ARIA=$who "$BIN" send -f --id "$T" -- "$text" >/dev/null 2>&1
    else
      "$BIN" send -f --id "$T" -- "$text" >/dev/null 2>&1
    fi
    wait_turn "$T" "$before" || { note="$note turn timed out;"; return 1; }
  }

  run "" "$ASK" && R1=$(newest_req "$T") && A1=$(reply "$T")
  run "$S" "$ASK" && R2=$(newest_req "$T") && A2=$(reply "$T")
  run "" "$CLOSE" && R3=$(newest_req "$T")

  verdict=$(python3 - "${R1:-}" "${R2:-}" "${R3:-}" "$SH" "$SS" "$ASK" "${A1:-}" "${A2:-}" <<'PY'
import json, re, sys
r1, r2, r3, handle, short, ask, a1, a2 = sys.argv[1:9]

def body(p):
    if not p:
        return None
    raw = open(p, encoding="utf-8", errors="replace").read()
    return raw.split("\r\n\r\n", 1)[-1] if "\r\n\r\n" in raw else raw.split("\n\n", 1)[-1]

def route(p):
    if not p:
        return "-"
    line = open(p, encoding="utf-8", errors="replace").readline().split()
    path = line[1] if len(line) > 1 else ""
    for k in ("messages", "chat/completions", "responses"):
        if k in path:
            return k.replace("chat/completions", "chat")
    return path[-10:] or "?"

def status(p):
    if not p:
        return 0
    try:
        return int(open(p.replace(".req.http", ".resp.http")).readline().split()[1])
    except Exception:
        return 0

def spellings(s):
    plain = json.dumps(s)[1:-1]
    html = plain.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026")
    return [plain, html, json.dumps(s, ensure_ascii=False)[1:-1]]

def spelled(s, *bodies):
    for sp in spellings(s):
        if any(b is not None and sp in b for b in bodies):
            return sp
    return spellings(s)[0]

b1, b2, b3 = body(r1), body(r2), body(r3)
tag = spelled('<system-reminder name="sender">\nfigaro ' + handle + ' (' + short + ')\n</system-reminder>', b2)
q = spelled(ask, b2)
problems = []
for n, p in ((1, r1), (2, r2), (3, r3)):
    if not p:
        problems.append(f"turn {n} left no wire dump")
    elif status(p) not in (200, 101):
        problems.append(f"turn {n} answered {status(p)}")

wire = "ok" if b1 is not None and b2 is not None else "-"
if b1 is not None and any(sp in b1 for sp in spellings('<system-reminder name="sender">')):
    wire = "FAIL"; problems.append("owner turn carries a sender reminder")
if b2 is not None:
    if b2.count(tag) != 1:
        wire = "FAIL"; problems.append(f"sender turn carries {b2.count(tag)} sender reminders")
    else:
        at, qa = b2.index(tag), b2.rfind(q)
        if not (0 <= at < qa):
            wire = "FAIL"; problems.append("reminder is not ahead of the text")

cache = "ok"
if b2 is not None and b3 is not None and tag in b2:
    at = b2.index(tag)
    qa = b2.find(q, at)
    run = b2[at:qa + len(q)] if qa >= 0 else None
    if run is None or run not in b3:
        cache = "FAIL"; problems.append("turn 2 is not resent byte for byte")
else:
    cache = "-"

def says(a, w):
    return re.search(r"\b" + re.escape(w) + r"\b", a, re.I) is not None

owner = "ok" if says(a1, "USER") and not says(a1, handle) else "FAIL"
if owner == "FAIL":
    problems.append(f"owner turn answered {a1.strip()[:60]!r}")
named = "ok" if says(a2, handle) or says(a2, short) else "FAIL"
if named == "FAIL":
    problems.append(f"sender turn answered {a2.strip()[:60]!r}")

print(route(r2 or r1), wire, owner, named, cache, "; ".join(problems), sep="\t")
PY
)
  IFS=$'\t' read -r rt w o n c why <<<"$verdict"
  printf '%-36s %-10s %-6s %-6s %-6s %-6s %s\n' "$pm" "$rt" "$w" "$o" "$n" "$c" "$note${why:-}"
  [ -z "$note$why" ] || fails=$((fails + 1))
done

echo
if [ "$fails" -eq 0 ]; then
  echo "PASS: every model saw the sender and the owner, and turn 2 was resent intact"
  [ "${KEEP:-0}" = 1 ] || { "$BIN" stop >/dev/null 2>&1; rm -rf "$ROOT"; }
else
  echo "FAIL: $fails model(s); store and wire dumps kept in $ROOT"
  exit 1
fi
