# The 516ns read is real. What it proves is not what it was cited for.

Aria 3a9225b1, 2026-08-18. Found while taking the stage-10 contention
baseline. **Nothing here says the code is wrong.** It says one number has been
carrying an argument it cannot carry, and stage 10 was about to be built on it.

## The claim as it stands in the record

`~/notes/layered-cache-design.md`, condition 1, and the memory campaign's
summary after it:

> cachedLog publishes its WHOLE resident window through an atomic.Pointer --
> ~420 rows on a real 2556-message aria -- and every read of recent-but-sealed
> history is served from it with NO LOCK. [...] a live-tail read is an atomic
> load (516 ns/op parallel, and it gets FASTER with more readers).

The evidence cited is `BenchmarkCachedLogReadFromParallel`, whose own comment
reads: *"The read the atomic.Pointer exists for [...] '34 acquisitions on the
hot read path, every one of which waited behind an append' is what this
measures the absence of."*

## What that benchmark actually does

It reads at a FIXED coordinate, `c.ReadFrom(1500, 64)`, against a writer
goroutine appending in a tight loop. The writer trims the window past 1500
almost immediately, and from then on every read falls THROUGH the published
view into `c.inner`, which is a plain mutex-guarded `MemLog`.

Two instruments, arrived at independently, agreeing to within 0.05 points:

    counted directly    195,702 of 200,000 reads fell below the window
                        = 97.9% into the locked inner log

    mutex profile       MemLog.ReadFrom holds 97.95% of 134.23s of delay
    (-cpu 16)           MemLog.Append holds 1.15%

The benchmark that measures "the absence of acquisitions on the hot read path"
spends 97.9% of its iterations on a path that acquires.

## And yet the scaling signature reproduces perfectly

My own baseline, this machine, median of 3:

    BenchmarkCachedLogReadFromParallel     cpu=1  1367 ns   cpu=4  572   cpu=16  555
    BenchmarkForestRangeParallel           cpu=1   968 ns   cpu=4 1179   cpu=16 1236

The published-view read really does get FASTER with more readers, and
forest.Range really does get slower. The shape the design rests on is exactly
as described.

**But it is not evidence of lock-freedom, because it is produced by a path
that takes a lock 97.9% of the time.** In a `RunParallel` benchmark ns/op is
wall time divided by completed operations; adding cores completes more
operations per unit wall time, so ns/op falls under contention too, until the
lock saturates. "Gets faster with more readers" is a throughput curve, not a
lock-freedom test, and it was read as one.

## The code is fine. This was checked separately, and binarily.

`internal/store/lockfree_probe_test.go` holds the lock and asks whether the
read still answers -- a fact, not a delta:

    MemLog.Read with MemLog.mu held             BLOCKED 2s   (canary: the probe works)
    cachedLog.ReadFrom inside the window        640 ns       LOCK-FREE
    cachedLog.TailAfter inside the window       5.36 us      LOCK-FREE
    cachedLog.ReadFrom BELOW the window         BLOCKED 2s   takes the inner lock

So condition 1 is TRUE OF THE CODE. The fast path is lock-free and the probe
proves it in the only way that is binary. The design's verdict, and the
refusal of `mem` under fig IR that rests on it, are unaffected.

## What is actually wrong, and what to do

1. **The benchmark mislabels itself.** Its comment claims it measures the
   absence of lock acquisitions; it measures their presence. Reading at a
   TAIL-RELATIVE coordinate instead of a fixed LT=1500 would keep it inside
   the window and make the comment true. That is a product change and is not
   mine to make; it is filed here for whoever owns it.
2. **The 516ns figure should be described as what it is**: the cost of a
   windowed read under a live writer, mostly served by the fall-through. It is
   a real and useful number. It is not "an atomic load".
3. **For stage 10 this is the whole point.** If the plan is "every read over
   append-only state takes no lock", then the benchmark that would be used to
   demonstrate success is currently measuring the locked path, and would show
   a large improvement for a conversion that changed nothing about the
   published view. A stage-10 claim must cite the zero-lock probe -- which
   cannot be fooled this way -- with contention numbers as secondary evidence
   only.

## The general form of the lesson

The reasoning was: this path is lock-free, lock-free paths scale with readers,
this path scales with readers, therefore the measurement confirms it. The
middle step is not reversible. A performance SHAPE is consistent with a
mechanism; it does not establish one. The mechanism was established later, by
holding the lock and watching the read answer anyway -- which took four lines
of test and two seconds, and should have come first.
