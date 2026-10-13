#!/usr/bin/env sh
# check-boundary.sh: the import-boundary ratchet from
# duck/2026-10-12-terraform-sandbox-federation.md §1 and §7.4 (T0 gate).
#
# Each rule is an edge `importer -> forbidden`. Most are violated today, so
# every violating edge is compared against scripts/boundary-allow.txt: an
# allowlisted edge passes, a new edge fails, and an allowlisted edge that no
# longer occurs fails too ("stale allow"), so the file only ever shrinks.
#
#   sh scripts/check-boundary.sh                # assert
#   sh scripts/check-boundary.sh --write-allow  # regenerate the allowlist
#
# Needs `go` on PATH; GOFLAGS=-mod=vendor makes it work offline under nix.
set -eu

cd "$(dirname "$0")/.."
MOD=github.com/jack-work/figaro
ALLOW=scripts/boundary-allow.txt
WRITE=0
[ "${1:-}" = "--write-allow" ] && WRITE=1

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export LC_ALL=C

# "prefix" semantics: the package itself or anything under it.
# $1 = package, $2 = prefix. internal/toolout does not match internal/tool.
under() {
	case "$1" in
	"$2" | "$2"/*) return 0 ;;
	esac
	return 1
}

# Emits "importer forbidden rule" per violating edge to $tmp/found.
found="$tmp/found"
: >"$found"
NRULES=0

# Rule 1 (transitive): cmd/figaro must not reach the daemon-side trees.
NRULES=$((NRULES + 1))
go list -deps -f '{{.ImportPath}}' "$MOD/cmd/figaro" | while IFS= read -r dep; do
	for p in internal/angelus internal/figaro internal/store internal/provider internal/tool internal/outfit; do
		if under "$dep" "$MOD/$p"; then
			echo "$MOD/cmd/figaro $dep cmd/figaro-reaches"
			break
		fi
	done
done >>"$found"

# Direct import lines for every package: "importer dep dep dep...".
direct="$tmp/direct"
go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... >"$direct"

# Rule 2 (direct): figwal must not import internal/ or api/. The tree may not
# exist yet; no packages is a pass.
NRULES=$((NRULES + 1))
if [ -d figwal ]; then
	go list -f '{{.ImportPath}} {{join .Imports " "}}' ./figwal/... | while IFS= read -r line; do
		importer=${line%% *}
		deps=${line#"$importer"}
		for dep in $deps; do
			if under "$dep" "$MOD/internal" || under "$dep" "$MOD/api"; then
				echo "$importer $dep figwal-imports"
			fi
		done
	done >>"$found"
fi

# Rule 3 (direct): internal/tool goes through api/sandbox, never exec or a pty.
NRULES=$((NRULES + 1))
grep "^$MOD/internal/tool " "$direct" | while IFS= read -r line; do
	importer=${line%% *}
	deps=${line#"$importer"}
	for dep in $deps; do
		case "$dep" in
		os/exec | github.com/creack/pty) echo "$importer $dep tool-imports" ;;
		esac
	done
done >>"$found"

# Rule 4 (direct): internal/cli is the thin client; no daemon-side imports.
NRULES=$((NRULES + 1))
grep "^$MOD/internal/cli " "$direct" | while IFS= read -r line; do
	importer=${line%% *}
	deps=${line#"$importer"}
	for dep in $deps; do
		case "$dep" in
		"$MOD/internal/angelus" | "$MOD/internal/figaro" | "$MOD/internal/figaro/wire" | \
			"$MOD/internal/store" | "$MOD/internal/store/xwal" | "$MOD/internal/tool" | "$MOD/internal/outfit")
			echo "$importer $dep cli-imports" ;;
		esac
		if under "$dep" "$MOD/internal/provider"; then
			echo "$importer $dep cli-imports"
		fi
	done
done >>"$found"

# Rule 5 (transitive): internal/daemon must not reach the TUI.
NRULES=$((NRULES + 1))
go list -deps -f '{{.ImportPath}}' "$MOD/internal/daemon" | while IFS= read -r dep; do
	if under "$dep" "$MOD/internal/cli"; then
		echo "$MOD/internal/daemon $dep daemon-reaches"
	fi
done >>"$found"

sort -u -o "$found" "$found"
cut -d' ' -f1,2 "$found" | sort -u >"$tmp/pairs"

if [ "$WRITE" = 1 ]; then
	cp "$tmp/pairs" "$ALLOW"
	echo "boundary: wrote $(wc -l <"$ALLOW" | tr -d ' ') allowed edges to $ALLOW"
	exit 0
fi

if [ -f "$ALLOW" ]; then
	grep -v '^[[:space:]]*\(#\|$\)' "$ALLOW" | sort -u >"$tmp/allow"
else
	: >"$tmp/allow"
fi

comm -23 "$tmp/pairs" "$tmp/allow" >"$tmp/new"
comm -13 "$tmp/pairs" "$tmp/allow" >"$tmp/stale"

fail=0
while IFS=' ' read -r importer dep; do
	[ -n "$importer" ] || continue
	rule=$(grep "^$importer $dep " "$found" | cut -d' ' -f3 | head -1)
	echo "boundary: NEW violation ($rule): $importer -> $dep" >&2
	fail=1
done <"$tmp/new"
while IFS=' ' read -r importer dep; do
	[ -n "$importer" ] || continue
	echo "boundary: stale allow, remove it from $ALLOW: $importer -> $dep" >&2
	fail=1
done <"$tmp/stale"

nallowed=$(comm -12 "$tmp/pairs" "$tmp/allow" | wc -l | tr -d ' ')
nnew=$(wc -l <"$tmp/new" | tr -d ' ')
nstale=$(wc -l <"$tmp/stale" | tr -d ' ')
echo "boundary: $NRULES rules, $nallowed allowed violations, $nnew new, $nstale stale"
exit $fail
