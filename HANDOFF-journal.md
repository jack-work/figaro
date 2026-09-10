# Handoff: feat/journal — three tasks

Branch `feat/journal` off `8ee80d7c` (v0.35.3), worktree
`~/dev/figaro-qua/queueform`. One commit: `1235aa32`. All suites green.

## 1. Measure the improvement (NOT YET DONE — do this first)

The fix is sound by construction and the suite is green, but "it should be
faster" is an impression, not evidence. Get a number.

Method (tmux-testing skill; `-count=1` is not optional):

```sh
cd ~/dev/figaro-qua/queueform
nix develop .#sandbox   # tmux lives here; it is NOT on the bare PATH by design
go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse --short=12 HEAD)" -o /tmp/j/after ./cmd/figaro
git stash && go build -o /tmp/j/before ./cmd/figaro && git stash pop   # or build 8ee80d7c
md5sum /tmp/j/before /tmp/j/after    # PROVE the arms differ (trap 11)
```

Then per arm, in a pane (invoke by ABSOLUTE PATH; `-e PATH=` is silently
ignored by tmux 3.6/3.7):

1. start a turn with a long tool round: `use bash to run: sleep 45, then say DONE`
2. wait ~12s so a tool is genuinely in flight
3. `:send -- SENTINEL`, then poll `capture-pane` every 200ms
4. record t_clear = when SENTINEL leaves the queue drawer
   and  t_visible = when SENTINEL appears as a steering node in the body
5. the gap is t_visible - t_clear

Expect `before` ~= the provider's TTFT for the next round (1-3s) and `after`
~= one frame. Report both numbers, not a verdict.

## 2. Audit the 15 emit sites for redundancy

```sh
grep -rn "a.emitDelta(\|a.emitCommit()\|ariaSrv.Commit(\|OpenInquiry(" internal/figaro/*.go | grep -v _test
```

The journal now publishes on EVERY durable append. Any site that emits
immediately after an append is now a second frame for one change. Nothing in
the suite caught one -- including the body-duplication smoke case, which
compares SEQUENCES not adjacency -- but it was not walked site by site.

For each: does an append precede it with no other state change between? If so
it is redundant. Be careful with `emitCommit()` (freezes the live unit -- a
different operation, usually NOT redundant) and with the streaming emits in
driveOneRound (different clock, must stay).

## 3. Trim the commenting

Gluck: "If something is reliably fixed and has not recurred, the aggressive
commenting can probably be removed."

Fair. Recent files over-explain. Keep the comment where it names a LAW or a
non-obvious constraint; cut it where it is a war story about a bug now covered
by a named test.

- KEEP: journal.go's "durability precedes visibility"; the two-clocks
  distinction; the no-recover() note (it explains why an obvious-looking line
  is absent); the gov==nil guard (non-obvious ordering).
- CUT/SHORTEN: the long measured-symptom paragraphs in intrinsic.go
  (publishRuntime), inbox.go (markCommitted), transcript.go (showQueuedAuto),
  intrinsic_mirror.go (the epoch note), session_status.go (beginTurn). Each has
  a test that fails with the same explanation; one line plus the test name is
  enough.

Rule of thumb: if deleting the comment would let someone reintroduce the bug
WITHOUT a test failing, keep it. Otherwise the test is the documentation.

## Still outstanding from earlier (Gluck's list, not started)

- shorter notice TTL (`config.NoticeTTL` has NEVER been read by anything)
- a `notifications` intrinsic form
- delete `inflight` from `/runtime` -- it is stale (only updates on turn
  transitions) and duplicates `/queue`'s `len`
