# Where the unmetered bytes are: the store opens before any aria exists

Aria 3a9225b1, 2026-08-18. The open item from the memory campaign, settled
with a heap profile on an isolated daemon. **Prediction was: <=5 call sites
account for >=60% of inuse_space. MEASURED: 2 call sites account for 63.71%.**

## The measurement

`nix develop .#snapshot` with the dev root forced onto btrfs (its default is
tmpfs -- see dev-root-in-ram.md), `FIGARO_PPROF=1`, a 417 MB copy of the real
aria store, and no live daemon touched.

    RSS            180.3 MiB      (VmHWM identical: no peak was missed)
    heap_alloc      90.2 MiB
    metered          0.0 MiB      resident_ir 0, translations 0, segments 0, ui_window 0
    resident arias   0
    goroutines      16

**A daemon with ZERO arias resident holds 90 MiB of heap that the meters
report as 0.0 MiB.** The gap is not a leak, is not the four caches, and is not
per-aria residency. It is paid before an aria exists.

## Where it is

    TOP inuse_space
      32.48 MB  45.97%  figwal/segment.JSONLCodec.ReadFrame
      12.53 MB  17.73%  encoding/json.(*RawMessage).UnmarshalJSON
       5.01 MB   7.10%  runtime.mallocgc
       3.00 MB   4.25%  reflect.mapassign_faststr0
       3.00 MB   4.25%  figaro/internal/form.makeNode
       1.06 MB   1.50%  store.(*XwalStore).topologySnapshot

    cumulative
      37.02 MB  52.39%  figwal/xwal.open
       7.08 MB  10.02%  figwal/disk.Open
      29.44 MB  41.67%  figwal/segment.(*Segment).cachedPayloads

Two call sites, 63.71%. Both are the WAL open-and-decode path, and 52% sits
cumulatively under `xwal.open`. The churn side says the same thing louder:

    TOP alloc_space
      0.48 GB  43.08%  bufio.NewReaderSize
      0.25 GB  22.16%  segment.JSONLCodec.ReadFrame
      0.12 GB  10.56%  segment.JSONLCodec.ScanFrames   (cum 0.64 GB, 57.03%)

**0.64 GB scanned through the JSONL codec to reach an idle daemon with nothing
open.**

## What this means for the meters

`figaro doctor mem` meters four caches, and all four are per-aria or
per-window structures that fill as conversations become resident. They are
accurate. They simply do not look at the store's own open cost, which on this
fixture is larger than everything they do measure.

That is why the ratio looked so alarming and so stable across samples. Four
observations of the same daemon software, same 417-423 MB store:

    arias   heap_alloc   metered   unmetered   source
      0        92 MB      0.0 MB     92 MB     isolated snapshot daemon (this note)
      5       146.6 MB   19.4 MB    127 MB     live daemon, 2026-08-17 11:44
      7       438   MB   42   MB    396 MB     live daemon, 2026-08-18 03:05, 15h uptime
      ?       830   MB   54   MB    776 MB     live daemon, 2026-08-16 (the original report)

**The unmetered part is present before the first aria** (92 MB at zero), and
it grows far faster than the metered part afterwards: from 0 to 7 arias the
meters gained 42 MB while the unmetered gained 304 MB. So the open cost
explains the FLOOR decisively, and something else -- churn the GC has no
pressure to collect under a 512 MiB soft limit, plus per-aria retention nobody
meters -- accounts for the slope. The floor is settled by this profile; the
slope is not, and saying which is which is the point of the table.

## What is NOT established here

- **Whether it scales with store size.** Strongly suggested (`ReadFrame`,
  `ScanFrames`, `cachedPayloads` are all proportional to what is on disk, and
  the fixture is a 417 MB store) but NOT measured. Settling it costs one run
  against a store of a different size, and until that is done the sentence is
  "92 MB on THIS store", not "92 MB per store".
- **Whether it is retained or merely uncollected.** `heap_alloc` 90 MB against
  `sys` 163 MB, with a 512 MiB soft GOMEMLIMIT, means the GC is under no
  pressure to give it back. Some of the 90 MB may be collectable garbage the
  runtime has no reason to collect.
- **Whether the original 830 MB had the same shape.** That daemon is gone. The
  shape reproduces at 0 arias and at 5; extrapolating to 830 MB is a guess.

## The recommendation, and it is small

The meters are not wrong, they are incomplete. One counter for the store's
resident open cost -- segment payload caches and decoded frames held by
`xwal.open` -- would move the unexplained fraction from ~100% to a small
remainder, and would make the ratio a diagnostic instead of a mystery. That is
a product change and is not mine to make.

## Aside, unresolved

Inside the snapshot shell, `figaro ls -g -j` returned no aria ids against a
417 MB seeded store, so the "open N arias and re-sample" arm measured nothing
and the before/after are identical by construction. The floor result does not
depend on it. But a listing that comes back empty against a populated store is
its own question, and it is recorded here rather than left as a gap in the
method.

---

## The slope: half of the hypothesis survives, and it is not the half with the arithmetic

Added 2026-08-18. 091d162e proposed that the unmetered bytes scale with
`loaded_heads` rather than with `resident_arias` -- 425 MB over 354 heads on
the live daemon is ~1.2 MB/head, and both hot profile sites are paid per open
thing rather than per aria. It was handed over to be killed rather than
confirmed. **It half survives.**

Measured directly instead of by regressing field observations: 48 channels
opened AND read against a cold-reopened store, `runtime.GC()` twice before
every sample, fixture built and closed before the baseline was taken.

    opened   heap MB   MB per open
         8      1.46         0.117
        24      3.13         0.108
        48      5.63         0.106
    MARGINAL over the second half:   0.104 MB per open

**SURVIVES: the cost is paid per open thing, and it is dead flat.** The
marginal open costs what the first one did, which is what "flat" has to mean
to be worth anything. The "not per aria" half of the hypothesis is confirmed.

**DOES NOT SURVIVE: the attribution to `loaded_heads`.** Across all 48 opens,
with reads, `LoadedHeads()` moved from **0 to 1**. figwal counts
`Trunks.hot.heads` plus retired -- **TRUNK heads** -- which is a different
population from the channels whose segments the heap profile names
(`JSONLCodec.ReadFrame`, `segment.cachedPayloads`, both per channel).

So `unmetered / loaded_heads` is not a per-head cost, and the tidy collapse of
the 0/5/7 table onto one line is **not established by this evidence**. The
number 1.2 MB/head divides a real quantity by a population that was not shown
to hold it.

WHAT WOULD SETTLE IT: a count of open CHANNELS on the live daemon, beside
`loaded_heads`, sampled at several points. `doctor mem` does not currently
report one -- which is the same gap as the missing open-cost counter, seen
from the other side. If open channels track the unmetered bytes at ~0.1 MB
each, the slope is explained and the meter has its second counter.
