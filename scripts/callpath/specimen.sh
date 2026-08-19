#!/usr/bin/env bash
# specimen.sh -- run the disk<->wire enumeration and leave ARTIFACTS, not a status.
#
# Every query writes its own file, stamped BEGIN and END with HEAD, dirty count,
# a digest of the diff, toolchain and loadavg. The stamps must AGREE or the run
# is inadmissible. The evidence is the FILE; the exit status of the runner is
# never the evidence.
#
# The tool is its own Go module, so it is BUILT there and RUN from the repo root:
# the package patterns below are figaro's, resolved against figaro's module.
#
# usage: scripts/callpath/specimen.sh <outdir>
set -u

OUT="${1:?usage: specimen.sh <outdir>}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
mkdir -p "$OUT"
cd "$ROOT" || exit 1

BIN="$OUT/callpath.bin"
( cd "$ROOT/scripts/callpath" && go build -o "$BIN" . ) || { echo "BUILD FAILED" >"$OUT/BUILD-FAILED"; exit 1; }

stamp() { # stamp <label> <file>
  {
    echo "# ${1} $(date -Is)"
    echo "#   HEAD        $(git rev-parse HEAD)"
    echo "#   BRANCH      $(git rev-parse --abbrev-ref HEAD)"
    echo "#   DIRTY       $(git status --porcelain | wc -l)"
    echo "#   DIFFDIGEST  $(git diff HEAD | sha256sum | cut -c1-16)"
    echo "#   GO          $(go version)"
    echo "#   LOADAVG     $(cut -d' ' -f1-3 /proc/loadavg)"
  } >>"$2"
}

run() { # run <name> <args...>
  local name="$1"; shift
  local f="$OUT/$name.txt"
  : >"$f"
  stamp BEGIN "$f"
  echo "# ARGV callpath $*" >>"$f"
  echo >>"$f"
  local t0=$SECONDS
  "$BIN" "$@" >>"$f" 2>&1
  local rc=$?
  echo >>"$f"
  echo "# EXIT $rc  SECONDS $((SECONDS-t0))" >>"$f"
  stamp END "$f"
  echo "ran $name rc=$rc secs=$((SECONDS-t0)) bytes=$(wc -c <"$f")"
}

# ---------------------------------------------------------------- READ, to the syscall
# The translator channel is decodeRecord[[]json.RawMessage]; the fig IR channel is
# decodeRecord[message.Message]. Both descend the same figwal subtree. The sink is
# the pread, which is reached ONLY ON A MISS -- the [CONDITIONAL] marking is the
# point of asking, not an aside.
run read-pread-vta  -pkgs ./internal/... -entry ProjectIncrementally -sink syscall.Pread -algo vta -deep -depth 40 -max 20
run read-pread-cha  -pkgs ./internal/... -entry ProjectIncrementally -sink syscall.Pread -algo cha -deep -depth 40 -max 20
run read-tree-vta   -pkgs ./internal/... -entry ProjectIncrementally -tree -treedepth 22 -deep -algo vta

# ---------------------------------------------------------------- WRITE, back to the syscall
run write-write-vta -pkgs ./internal/... -entry 'xwalLog' -sink syscall.Write -algo vta -deep -depth 40 -max 20
run write-write-cha -pkgs ./internal/... -entry 'xwalLog' -sink syscall.Write -algo cha -deep -depth 40 -max 20
run write-tree-vta  -pkgs ./internal/... -entry 'xwalLog[T]).Append' -tree -treedepth 22 -deep -algo vta

# ---------------------------------------------------------------- HOLE 1, the fourth reading
# config.Encode is a FUNC VALUE IN A STRUCT FIELD. VTA silent + CHA connected is
# the finding; the two runs answer different questions and the DIFFERENCE is it.
run hole1-encode-vta -pkgs ./internal/provider/... -entry ProjectIncrementally -sink renderPatchBlocks -algo vta -max 40
run hole1-encode-cha -pkgs ./internal/provider/... -entry ProjectIncrementally -sink renderPatchBlocks -algo cha -max 40

echo "SPECIMEN COMPLETE $(date -Is)" >"$OUT/DONE"
