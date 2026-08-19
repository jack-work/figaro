# HANDOFF FROM dec6ef8a, 2026-08-19

You hold @980dc16c. Read plans/tree-shaped-log.md top to bottom first: its
opening sections are Gluck's standing orders and they govern everything below.

## FIRST FOUR ACTIONS

1. `figaro cast @980dc16c` -- NEVER bind; a bind is a frozen copy with no study.
2. `figla arm --aria @980dc16c --in 25m --about "<DISTINCT TEXT>"` -- distinct
   text matters: figla derives its unit name from a truncated prefix and two
   reminders with the same leading words COLLIDE.
3. `figaro set --id <your id> mantra "..."`.
4. Read plans/tree-shaped-log.md, then this file's OPEN WORK.

## WHERE THE TREE IS

MAIN IS THE ONLY LINE. `feat/layered-cache` is merged and dead; anyone still
cutting from it is on a stale head (041454f1 was, and rebased). Worktree
/home/gluck/dev/figaro-qua/main, head b5860000, clean, full suite and -race
green, `nix build` green.

## WHAT LANDED TODAY, IN ONE LIST

  RESIDENCY OWNERSHIP    the store owns its defaults; config TUNES them and now
                         owns no residency number at all.
  THE ESTIMATE           irDecodeInflation was 5 and measures 1.35 on Gluck's
                         real history. Budget moved with it (4 MiB -> 1 MiB of
                         the same real memory). Estimate-vs-heap 0.96.
  THE CANONICAL CACHE    reads take no lock; Range hands back a VIEW where one
                         run answers; lookups guess arithmetically and verify;
                         density is checked once at publication; runs are cut by
                         BYTES, not by count.
  THE SEGMENT TENANT     its duplicate residency structure is deleted.
  THE DECODED IR AND     both on tree.Cache: a node per aria, one budget, one
  THE TRANSLATIONS       eviction order. cachedLog is DELETED (-176 non-test,
                         -857 test). A fork's prefix is its ancestor's runs --
                         the donation and its seam probe are gone.
  EVICTION               a read NEVER evicts. charge raises pressure; the
                         daemon's standing sweep (angelus.pidMonitor, 2s) lowers
                         it, through the same interface the segment cache
                         already used. Budget.Settle is for tests and doctor.
  THE IR DOOR            one write path into the fig IR, enforcing the tool-call
                         invariant at the point of writing: a message may not
                         land while results are outstanding, a partial set is
                         completed IN PLACE, a late result becomes a system note
                         rather than a failure, and ceremony closes nothing.
  EPHEMERAL ARIAS        cut entirely. Every aria is backed; NewAgent panics
                         without a Backend; store.NewTestBackend/NewTestAria are
                         how a test gets one.
  FORM INPUT             applied at SUBMIT time, so a refusal reaches the caller
                         instead of being logged after the RPC returned.
  `cwd`                  deleted: system.cwd is canonical and harness-written.
  COMMENTS               7,427 lines of inline justification purged.
  DEAD CODE              11 statically dead functions deleted; standing approval
                         to delete more without asking.

## OPEN WORK, IN THE ORDER I WOULD TAKE IT

1. **Q3's LAST TENANT: `internal/livelog/aria.TurnCache`.** It is tree's design
   written a third time -- hollow entries, an index that survives eviction, a
   source that rematerializes, per-turn pinning, and it quotes the same
   post-mortem in the same words. Measured: it faults within 1.3% of tree once
   run granularity is matched. ITS COMPLICATION IS A POSITIONAL API where the
   other two tenants were LT-keyed; decide whether the Server moves to
   coordinates or the tenant keeps a slim ordered key list.
2. **Q2, ENDORSED BY GLUCK, UNSTARTED: epoch buckets** for eviction. Today
   `coldest`+`evictColdest` are 2R run visits per eviction and the sweep is
   R^2 (measured). He endorsed buckets over a heap; MEASURE bucket maintenance
   against the scan it replaces before building.
3. **The segment scan's 8 bytes.** The dedup costs +31% on a sequential scan
   because the tenant's unit carries the key the Keyer needs (32 B against a
   bare 24 B slice header). Gluck accepted it. The cure is a nil-Keyer
   "positional mode" where the Source must fill the coord exactly.
4. **041454f1** is on feat/nested-form-values, rebased onto 776f4342; it must
   move to current main and will feel the eviction change (fixtures that assert
   a bound right after a read must ask for the sweep).

## THE STANDARDS, WHICH ARE NOT MINE TO SOFTEN

Gate logs stamp BEGIN and END with head, dirty count, digest, toolchain and
load, and they must AGREE. Assert the ARTIFACT, never a status field, and NEVER
`&& echo OK` after a pipe -- read the status from the process. Canary every
test: A PASSING CANARY IS A FINDING, and a passing canary WITH AN EXPLANATION
ATTACHED is a finding that has been talked out of existence. Count rather than
time where the question is "how many times". Corrections go in NEW paragraphs
beside what was read. The claim is written AFTER the verification.

AND THE MEASUREMENT RULES I PAID FOR TODAY:
  INTERLEAVING IS NOT COUNTERBALANCING. An A/B without an A/A is an
  uncalibrated instrument -- I published a -43.8% that does not reproduce and
  had to retract it (e124f064). Prefer TWO BENCHMARKS IN ONE BINARY with an
  in-run control; report the deterministic quantity (allocations, counts) ahead
  of the timing.
  A SINGLE-SHOT HEAP DELTA IS AN UNREPEATED MEASUREMENT. Run three.
  A FACTOR CALIBRATED ON THE EIGHT BIGGEST ARIAS IS A FACTOR FOR THE EIGHT
  BIGGEST ARIAS.

## WHAT I GOT WRONG, SO YOU EXPECT IT OF YOURSELF

I published a speedup that did not reproduce. I built a per-Handle hint that
made parallel reads 6.5x worse by reintroducing the per-read shared write this
package was founded on refusing -- I had quoted that law the same morning. I
wrote a background sweeper with its own goroutine and lifecycle when a standing
reaper already existed, and Gluck refused it. I calibrated a constant on the
largest arias and shipped it 13% low. I gave one cache per log and shared
nothing. I keyed a channel on a foreign key. I let a mechanical comment purge
cherry-pick sentences out of paragraphs and leave hundreds of fragments. Every
one of those was caught by an instrument or by Gluck within the hour, which is
the arrangement working -- and none of them was caught by re-reading my own
diff.

RULE FAST ENOUGH TO BE WRONG EARLY. If a turn passes with nobody correcting
you, be suspicious of the arrangement rather than pleased with it.
