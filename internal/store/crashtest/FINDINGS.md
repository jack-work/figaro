# Crash-durability findings against e2f5c89

Campaign: `crashtest` harness (see below) run against the tree at e2f5c89
("feat: add crash-safe dynamic XWAL channels"), 2026-07-23.

- Kill mode: ~1400 SIGKILL cycles (`-race`, `-long`, seeds 11/12/13 plus
  reruns and short sweeps). Kill delays biased sub-100 ms to catch
  mid-operation windows.
- Corrupt mode: 168 cycles (seeds 21/22/23, `-long`): after each kill, the
  tail region of segment files written during the cycle is randomly
  truncated / overwritten / appended / byte-flipped before reopen,
  simulating power-loss torn writes.

Replay: `go test ./crashtest -run <Test> -seed=<N> -long`. The seed fully
determines the op schedule, kill delays, and corruption sites; kill-window
findings are races against wall-clock delays, so a replay reproduces the
same distribution (and usually the same findings) rather than the exact
cycle indexes. Occurrence counts below are from the recorded campaign logs.

## Kill mode (pure SIGKILL: completed writes survive in the page cache)

### K1. Fork commit is not crash-atomic: trunk id lost

`trunk-lost-mid-fork`, 6 occurrences. SIGKILL during `Trunks.ForkAt`
(freeze + continuation/alternative commit) leaves the SOURCE trunk id
unresolvable after reopen: its records are still on disk but the lineage
cannot be addressed. Worst case the store's only trunk vanished and the
next writer found zero trunks.

- seed=11 store=4 cycle=5 childSeed=5391402773547066655 (also stores 6, 8, 12)
- seed=12 store=0 cycle=3 childSeed=8078290730781643978; store=1 cycle=1

### K2. Stuck `.fork-pending` sentinel bricks the store

`open-failed`, 3 occurrences. fork.go claims "Open rolls a partial fork
forward"; in practice a mid-fork SIGKILL can leave a `.fork-pending`
sentinel that every subsequent `OpenTrunks` refuses:

- seed=11 store=6 cycle=3 childSeed=7342284354246113453, sentinel left in
  a related-channel branch dir (`notes/n0/n2/n3`); open fails "fork in
  progress" permanently.
- seed=12 store=2 cycle=1 childSeed=6759699898442884760, sentinel in
  `notes/n0`, same permanent failure.
- seed=12 store=11 cycle=2 childSeed=4073270498405317002, recovery is not
  idempotent: "recover interrupted fork: fork \"main\": open: fork in
  progress: dir contains .fork-pending sentinel: .../main". The roll-forward
  path itself trips over the sentinel it is supposed to consume.

### K3. Mid-fork kill can cut the HEAD of a related channel

`head-cut`, seed=12 store=2 cycle=1: after reopen the notes channel starts
at chanLT 2, record 1 is gone. Loss from the front, so the surviving log
is not a prefix at all.

### K4. Positive results

Across all kill cycles, zero occurrences of: lost completed appends, lost
synced records (`SyncChannel` honored), checksum-invalid payloads served,
gaps or renumbering outside fork windows, panics. Append / rotation / sync
crash windows look solid at e2f5c89; fork commit is the one broken window.

## Corrupt mode (torn-tail simulation)

Repair at e2f5c89 assumes only the last segment of the writing node can be
torn. A torn tail in any OTHER recently written segment, one sealed by
rotation during the cycle, or a fork-parent segment, is not detected:

### C1. Silent renumbering

`prefix-mismatch`, 43 occurrences. Entries after the torn segment are
reassigned contiguous lower indexes on reopen: main identity
(mainLT == chanLT) breaks, records are served at wrong LTs, and later
writers die with "write index out of order". Long
`main identity broken at N: mainLT N+1` runs in the seed 21/22/23 logs.

### C2. Interior holes

`chanlt-gap`, 42 occurrences. The served channel skips indexes (e.g.
"record 22 has chanLT 25"): neither a clean prefix nor contiguous.
Example: seed=23 store=5 cycle=0 childSeed=1148012427350079557.

### C3. Store bricked on open

`open-failed`, 8 occurrences (+3 in kill mode via K2). `OpenTrunks` fails
with "index not found" and there is no repair path.
Example: seed=22 store=3 cycle=0 childSeed=5771674511721379601 (byte flip
in a reducible watermark segment `state/n0/...0002.jsonl`).

### C4. No lineage-coherent cut

`orphan-related` 20, `reducible-ahead` 8. Channels are repaired
independently, so after a main-channel tail truncation the related and
reducible channels keep records referencing main LTs that no longer exist.
This is precisely the coherent-cut repair the memory-first contract adds.

### C5. Head cut served silently

`head-cut`, 5 occurrences. Torn first segment of a channel: the suffix is
served with no error. Example: seed=21 store=3 cycle=0
childSeed=920590367903051027 (notes starts at chanLT 3).

### C6. Positive results

Zero panics and zero corrupt payloads served as valid across all
corruption cycles: the JSONL per-line hash reliably catches byte damage,
and damaged records are dropped rather than returned mangled. Failure
modes are availability (brick) and placement (renumber/hole/head-cut),
never undetected payload corruption.

## Harness notes

- Writer child (test-binary re-exec) appends self-describing checksummed
  records across main + notes + reducible state channels on up to 5
  trunks with occasional forks, reporting each completed op over stdout;
  the harness SIGKILLs it at randomized delays, reopens in-process, and
  checks: checksums, per-channel clean-prefix against the reported ops,
  synced lower bound, lineage coherence, reducible fold.
- Binding to the e2f5c89 API is confined to `bind_trunks.go` (config,
  open/create, `storeGaps`); rebinding to the OpenStore memory-first API
  is a one-file change.
- Known-gap classes (this file) are reported as `FINDING` log lines and
  kept green so the suite stays a regression net; anything outside them
  fails the test. Kill-mode gaps: `append-lost` (contract-permitted flush
  lag; zero occurrences observed) and, only while a fork is in flight,
  the K1-K3 wreckage classes. Corrupt-mode gaps: C1-C5.

---

# memory-first findings (branch memory-first merged at 8b888d7)

Harness rebound to `xwal.OpenStore` (bind_trunks.go): no SyncChannel, the
durable lower bound is time-based (append stamp older than
FlushInterval+slack before the kill must survive), plus Close-drain
(everything durable once Close returns) and Kick as a scheduling hint.
Writer child adds: voluntary clean-close cycles, kill-mid-Close cycles,
and concurrent peer-goroutine bursts on other trunks (adoptLineage races).

Campaign: 808 `-race -long` kill cycles (seeds 31/32/33), 109 corrupt
cycles (seeds 41/42/43), plus the targeted flush-error test.

## Confirmed STILL BROKEN

### M1 = K1. Trunk id lost on mid-fork SIGKILL (unchanged)

`trunk-lost-mid-fork`, 4 kill-mode + 4 corrupt-mode occurrences.
- seed=31 store=1 cycle=0 childSeed=8203147640639102036 delay=145ms
- seed=32 store=1 cycle=1 childSeed=7004715793822147520
- seed=33 store=5 cycle=1 childSeed=1020459635174625407; store=7 cycle=1

### M2 = K2. Stuck `.fork-pending` sentinel still bricks the store

seed=33 store=7 cycle=1 childSeed=602310214271270794: after a mid-fork
kill, reopen fails "fork in progress: dir contains .fork-pending
sentinel: .../state/n0", permanently. 1 occurrence in 808 kill cycles.

### M3 = C1-C5. Torn tails outside the active segment still misplace data

Corrupt-mode classes and counts over 109 cycles: renumbering
`prefix-mismatch` 102, interior holes `chanlt-gap` 52, `open-failed`
brick 5, `orphan-related` 9, `reducible-ahead` 7, `head-cut` 2,
`read-failed` 8, `state-fold` 2. The coherent-cut repair trims clean
crash tails but does not detect a torn SEALED segment (rotated or
fork-parent); subsequent records renumber or the store bricks, same
failure shapes as e2f5c89.

### M4. Repair is gated on `.unclean`: corruption after clean Close slips through

A1's declared risk, confirmed: 34 of the damaging corrupt cycles were
`cleanclose` (child Closed cleanly, then bytes were torn, then reopen):
OpenStore skips repairCoherentCuts entirely and serves/renumbers/bricks
exactly as if unrepaired. Example: seed=42 store=14 cycle=0
childSeed=2833144618262241349 (flip in `main/n0/...0039.jsonl`).

## Confirmed FIXED relative to e2f5c89

- Lineage coherence after pure crash: ZERO `orphan-related` and ZERO
  `reducible-ahead` in 808 kill cycles (e2f5c89 produced both after any
  main-tail repair). The vectored coherent-cut flush works.
- Kill-mode head-cut (K3): not observed in 808 cycles.
- Durability contract: zero `durable-lost`, every append older than
  FlushInterval(+500 ms slack) before the kill survived; Close-drain
  lost nothing (all cleanclose kill-mode cycles verified full survival);
  kill-mid-Close cycles repaired cleanly on reopen.
- Flush-error path: TestFlushErrorReadOnlyRecovers, segment dirs made
  read-only mid-run: appends keep succeeding in memory, flusher retries,
  full recovery after permissions restored, clean Close, zero loss.
- Peer-goroutine append/fork interleaving: no divergence, no violations
  attributable to concurrent lineage flushes.
- Single-writer flock + `.unclean` lifecycle: reopen-after-SIGKILL always
  acquired the lock (kernel releases flock on death); kill between
  markUnclean and Close always left the marker, so repair ran.

## Expected-loss profile

`append-lost` findings (contract-permitted flush lag): ~580 per 300-cycle
kill run, nearly always 1-3 records per channel, the in-memory tail
since the last flusher pass. e2f5c89 showed zero here (its writes hit the
page cache synchronously); this is the traded loss window of
memory-first, bounded and honored.

## Suite classification after this campaign

Kill-mode known gaps: `append-lost`, `trunk-lost-mid-fork` (M1), plus
fork-wreckage classes only when a fork was in flight (M2). Corrupt-mode
known gaps: M3/M4 classes. Everything else, durable-lost, checksum,
panic, mismatch/gap/head-cut outside fork or corruption windows, fails
the suite.
