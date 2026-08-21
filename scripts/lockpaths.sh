# WHICH READS TAKE A LOCK, AND WHERE.
#
# The standing goal is "reduce the mutexes", and the only instrument for it has
# been a count of DECLARATIONS -- 83 at the campaign's start, 81 now. That
# number measures how many locks EXIST. The standing order is about something
# else: "be on the lookout for spurious locking, even where it appears
# valuable", which is a question about locks TAKEN ON A READ PATH.
#
# This answers that one, statically, with scripts/callpath. It is a TOOL and
# not a list, for the reason callpath's own README gives: a list is a snapshot
# of someone's reading and it is wrong within a day.
#
#   ./scripts/lockpaths.sh                 the standard read entries
#   ./scripts/lockpaths.sh 'treeLog.*Read' just one
#
# READ THE CAVEATS callpath prints. In particular an ABSENT path is not proof
# of a lock-free read: the bytes may cross by value, or the symbol may sit
# outside the cut. This finds locks; it does not certify their absence.
set -uo pipefail
cd "$(dirname "$0")/.."

BIN=$(mktemp -d /var/tmp/lockpaths.XXXX)/callpath
( cd scripts/callpath && go build -o "$BIN" . ) || exit 1

# The read surface worth asking about: the whole-channel read, the bracket
# read, the point read and the tail read, on both decoded channels.
ENTRIES=${1:-'RawMessage]).Read|message.Message]).Read|RawMessage]).ReadFrom|RawMessage]).Lookup|RawMessage]).PeekTail'}

echo "# LOCKS REACHED FROM A READ, via scripts/callpath (vta, deep, depth 7)"
echo "# An absent path is NOT proof of a lock-free read -- see the tool's own header."
echo

IFS='|' read -ra PATS <<< "$ENTRIES"
for pat in "${PATS[@]}"; do
  out=$("$BIN" -pkgs ./internal/store/... -entry "$pat" -sink "Mutex).Lock" \
          -deep -depth 7 -algo vta -max 40 2>/dev/null)
  # The lock site is the LAST frame of each path; the caller above it is what
  # a reader needs in order to find it.
  sites=$(echo "$out" | grep -B 1 "sync.Mutex).Lock\|sync.RWMutex).Lock" \
          | grep -oE "internal/store/[a-z_]+\.go:[0-9]+" | sort -u)
  n=$(echo "$out" | grep -c "ROOT ")
  if [ -z "$sites" ]; then
    echo "  $(printf '%-34s' "$pat") no lock reached in $n path(s)"
  else
    echo "  $(printf '%-34s' "$pat") $n path(s), locks at:"
    echo "$sites" | sed 's/^/      /'
  fi
done

echo
echo "# The lock SITES above are where to look. What each lock is FOR, and"
echo "# whether its critical section holds an invariant that could be published"
echo "# instead, is plans/store-locks.md's question and not this script's."
rm -rf "$(dirname "$BIN")"
