# The storage is made of the resource being measured

**Both defaults are correct for their original purpose and wrong for
measurement, and neither announces itself in any output.**

That is the reusable half of this note. The rest is four instances of it.

Aria 3a9225b1, 2026-08-18, with two members found by 091d162e on inspection.

## The family, three-for-three: the fallback is always tmpfs, and the log never says so

| where | falls back to | fs | today |
|---|---|---|---|
| `b.TempDir()` in every WAL-writing benchmark | `/tmp` | tmpfs 28G | **active**: store benchmarks timed a RAM disk |
| `FIGARO_DEV_ROOT` (flake.nix:237) | `$XDG_RUNTIME_DIR` | tmpfs 5.5G | **active**: `.#snapshot` copies 423 MB of arias into RAM |
| `internal/cli/angelus_client.go:47` | `os.TempDir()/figaro` | tmpfs | benign: only when `XDG_RUNTIME_DIR` is unset, and it is a socket |
| `internal/outfit/bundled.go:120` | `os.TempDir()/figaro-state` | tmpfs | benign: only when the home dir cannot be resolved; 9.6 MB, re-unpacked per process |

The last two are **not defects and are not urgent**. A socket belongs in a
runtime dir, and a fallback that works when `$HOME` is unresolvable is doing
its job. They are here because the next person to measure something will meet
one of them, and because the pattern is what matters: the fallback is always
tmpfs and nothing in any output says so.

## The instance that would have beaten the measurement

`flake.nix`, the dev-shell helper every preset is built from:

    export FIGARO_DEV_ROOT="${XDG_RUNTIME_DIR:-/tmp}/figaro-dev-${name}"

`XDG_RUNTIME_DIR` here is `/run/user/1000`: **tmpfs**, 5.5G total, 381M
already used. So a dev root is a RAM disk, and `nix develop .#snapshot` --
whose entire job is to `cp -a` the real aria store into it -- copies **423 MB
of arias into RAM**.

## Why it would have beaten it quietly, which is the point

The snapshot shell is the sanctioned way to investigate the daemon's memory
without touching the live daemon. Run as designed, it would have had me
measuring a daemon's RSS **while 423 MB of that daemon's own store sat in the
same RAM**.

**And it would not have appeared in `VmRSS` at all.** tmpfs pages are charged
to the system, not to the process. The metered/unmetered gap is precisely the
quantity at stake, so this is not noise -- **it is a bias with a sign**, and
every number would still have looked plausible.

## A second, smaller defect beside it

`FIGARO_DEV_ROOT` is **hard-exported**. Every other knob in the same helper is
written `: "${VAR:=...}"` with a comment explaining the choice:

> All branches use `: "${VAR:=...}"` instead of `export VAR=...` so a pre-set
> env var from the caller's shell wins. This makes the presets composable from
> outside.

Zero of the file's assignments actually follow that rule for the dev root
itself, so the one variable that decides WHERE EVERYTHING LANDS is the one
variable a caller cannot set. The workaround is to override `XDG_RUNTIME_DIR`
instead, which works only because the dev root is derived from it:

    XDG_RUNTIME_DIR=/var/tmp/figaro-snap nix develop .#snapshot

That is a side effect standing in for a knob.

## Recommended (product change; NOT mine to make)

1. `: "${FIGARO_DEV_ROOT:=...}"` like its siblings, so it composes.
2. Default it somewhere disk-backed, or refuse tmpfs when the preset is one
   that copies a store (`snapshot` is the only one that does, and it is the
   one that copies hundreds of MB).
3. At minimum, print the dev root and its filesystem on shell entry. The
   snapshot preset already prints `copying 423M from ...`; adding `into
   /run/user/1000 (tmpfs)` would have made this self-evident.

## What the fix is actually worth

Of the three stage-0 commits, this one voids no comparison and is the least
urgent. Its most valuable line is not the fix at all: the snapshot shell
ALREADY prints `copying 423M from ...`. Appending `into /run/user/1000
(tmpfs)` would have made this self-evident to me and to everyone after me,
without anyone having to read flake.nix. **A default that announces its own
filesystem cannot become this note.**

The measurement gate now sets `TMPDIR=/var/tmp` in every panel and pins
`XDG_RUNTIME_DIR` before entering the snapshot shell, and `daemon-rss.sh`
asserts its dev root is not on tmpfs rather than trusting it.
