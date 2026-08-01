# Testing the trunk capability

Two hierarchies, one optional. Everything here exercises both.

    TOPOLOGY     .from + per-channel .fork base, on disk. Where history
                 comes from. figwal owns it. Always present.
    PRESENTATION internal/topo.Tree. What `figaro ls` draws and what a
                 delete takes. Optional: `trunks = false` removes it.

## The matrix

`internal/figaro/wire/matrix_test.go` runs the store with the capability on
AND off. The off case is the point: it is the only thing that proves the
dependency is genuinely optional.

    go test ./internal/figaro/wire/ -count=1 -v

`internal/trunk/isolation_test.go` fails and NAMES any package that starts
importing `internal/trunk` outside the wiring. If you must add one, add it
to `allowed[]` knowing a trunkless figaro can no longer build it.

    go test ./internal/trunk/ -run TestTrunkCapabilityStaysALeaf

## Dev shells

    nix develop                # normal dev shell, isolated runtime
    nix develop .#snapshot     # a COPY of the real store, for migration
                               # and real-shaped-data work

`.#snapshot` reseeds from your daemon's arias into `$FIGARO_STATE_DIR`. It
is a copy. Migration testing goes here and ONLY here — never against
`~/.local/state/figaro/arias`, because a bad migration cannot be undone by
rebuilding.

    figaro-snapshot-reseed     # take a fresh copy

## Turning it off

    # config.toml
    trunks = false

Then the hierarchy IS the fork topology: `figaro promote` reports that the
build cannot serve it, `figaro ls` still draws parent relationships (from
`.from` instead of the pstate), and a delete can never orphan a survivor
because the two trees agree by construction.

Verify a trunkless daemon really never constructs a pstate:

    trunks=false; start daemon; create + fork some arias
    test ! -e "$FIGARO_STATE_DIR/arias/trunks.json"

## CLI under mutation

The CLI must stay correct while the tree moves underneath it. Drive the
real binary in a real pty (see the tmux-testing skill), promote from a
second shell, and confirm the pager and `ls` stay coherent:

    tmux new-session -d -s trunk 'figaro'
    figaro promote <id>
    tmux capture-pane -p -t trunk

Then run the SAME script without tmux. Anything that only reproduces one
way is a tty dependency, not a trunk bug.

## Long arias

Synthesise rather than generate: copy entries from a COPY of the real store
into a scratch store under `/var/tmp`. Deterministic, and far faster than
driving a model.

    nix develop .#snapshot
    # then fork/promote/delete against $FIGARO_STATE_DIR

## Crash safety

figwal's crash harness is the race engine — it kills a child at randomised
offsets and replays by seed:

    cd $FIGWAL && go test ./crashtest/ -count=1          # both modes
    cd $FIGWAL && go test ./crashtest/ -count=1 -long    # 600 kill cycles

Rules learned the hard way, all of them from real defects:

  - COUNT, DO NOT TIME. Assert rebuild/segment/rename counts. A timing
    assertion passes on ext4 and fails only on someone's NTFS box.
  - CANARY EVERY TEST. After it passes, re-break the thing it covers and
    confirm it fails for the stated reason. A test that cannot fail is
    decoration.
  - Run the harness in a SEPARATE worktree. Editing the tree while a
    background run compiles produces "failures" that are build errors.
  - Re-run the test that ORIGINALLY failed, never a proxy.
