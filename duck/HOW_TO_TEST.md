# Testing the xwal fork tree end to end

Everything runs against an **isolated** daemon (its own runtime + state
dirs) so it never touches your live figaro. Config, hush, and auth are
inherited from your normal setup, so real provider turns work.

## Option A — nix dev shell (recommended)

The flake's dev shells put **this worktree's build** on `PATH` as
`figaro` / `fig` / `q`. Use the `share-config` preset: it isolates
runtime + state but inherits your real config + hush (so providers and
keys just work).

```fish
cd ~/dev/figaro-qua/xwal-forking
nix develop .#share-config
# inside the shell, `figaro` is the worktree build; RT/ST are dev-scoped:
#   FIGARO_RUNTIME_DIR = $XDG_RUNTIME_DIR/figaro-dev-share-config/run
#   FIGARO_STATE_DIR   = $XDG_RUNTIME_DIR/figaro-dev-share-config/state
```

Then jump to step 3 below (`figaro list`, a turn, `fork`, …). The fork
tree lives under `$FIGARO_STATE_DIR/arias/`. To start from a blank slate,
`rm -rf $FIGARO_STATE_DIR` (or use `nix develop .#clean` for fully
hermetic incl. a fresh hush + first-run).

Note: the dev-shell state dir persists across shell entries (it's a
stable path, not a fresh tempdir) — delete it when you want a clean run.
`fig stop` / `pkill -f figaro` stops the dev daemon.

## Option B — plain go build + tempdirs

## 1. Build

```fish
cd ~/dev/figaro-qua/xwal-forking
go build -o /tmp/figt ./cmd/figaro
```

## 2. Isolated env (paste into the shell you'll test from)

```fish
set -x FIGARO_RUNTIME_DIR (mktemp -d /tmp/figrt.XXXX)
set -x FIGARO_STATE_DIR   (mktemp -d /tmp/figst.XXXX)
```

The first `/tmp/figt` command auto-spawns the daemon with these dirs.
The aria fork tree lives under `$FIGARO_STATE_DIR/arias/`.

## 3. Drive it

```fish
/tmp/figt list                      # empty table; boots the daemon + null root
/tmp/figt "what is 2+2?"            # creates a conversation (real LLM turn), binds this shell
/tmp/figt state                     # chalkboard snapshot (credo, cwd, aria_id, ...)
/tmp/figt set note hello            # append a chalkboard transition
/tmp/figt "and 3+3?"               # second turn; the `note` reminder rides this tic
/tmp/figt show -v                   # raw IR incl. the <system-reminder> transitions
/tmp/figt list                      # one conversation, with msgs/tokens/cwd
```

## 4. Fork

```fish
/tmp/figt fork                      # freezes the bound aria, mints two children;
                                    # this shell rebinds to the continuation
/tmp/figt list                      # the parent is now a frozen node; children present
/tmp/figt "continue here"           # extends the continuation
/tmp/figt show                      # shared prefix (both prior turns) + the new turn
```

Diverge the alternative (id printed by `fork`):

```fish
/tmp/figt attend <alt-id>           # bind this shell to the alternative
/tmp/figt "go a different way"      # the alternative diverges; continuation untouched
/tmp/figt show --id <cont-id>       # continuation still has only its own turn
```

## 5. Verify the tree on disk

```fish
cat $FIGARO_STATE_DIR/arias/index.json   # nodes (null -> loadout -> conversations), frozen flags, loadouts map
```

You should see: one `null` root (`"arias"`), one `loadout` node per
(name, content-version), and conversation nodes — the forked parent with
`"frozen": true` and two children pointing at it via `parent`.

## 6. Tear down

```fish
pkill -f /tmp/figt          # stop the isolated daemon
rm -rf $FIGARO_RUNTIME_DIR $FIGARO_STATE_DIR /tmp/figt
```

## What to look for / known rough edges

- **Loadout reminders render once** in the shared loadout prefix (the
  loadout node's renderable birth tic) and are inherited by every
  conversation — check `show -v` on a second conversation from the same
  loadout: its skills/credo come from the cached prefix, not re-injected.
- **`set` then immediately `fork`** with no turn in between: the pending
  patch rides only the continuation (known edge — commit a turn between
  `set` and `fork` for it to reach both).
- Dormant `figaro list` tokens/recency come from the `_meta/<id>.json`
  sidecar written at end of each turn.

## Automated coverage (no LLM, free)

```fish
go test ./internal/store/ ./internal/figaro/ ./internal/angelus/
```

`TestIntegration_Fork` (angelus) drives create → turn → fork → verifies
shared prefix + distinct children + frozen parent with a mock provider.
